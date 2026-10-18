package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/seeker/polymarket-bot/internal/api"
	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/internal/logging"
	"github.com/seeker/polymarket-bot/internal/metrics"
	"github.com/seeker/polymarket-bot/internal/order"
	"github.com/seeker/polymarket-bot/internal/paper"
	"github.com/seeker/polymarket-bot/internal/strategy"
	"github.com/seeker/polymarket-bot/internal/websocket"
	"github.com/seeker/polymarket-bot/pkg/types"
)

var (
	configPath = flag.String("config", "config.yml", "Path to configuration file")
	paperFlag  = flag.Bool("paper", false, "Force paper trading (simulated orders, no real money)")
	colorFlag  = flag.String("color", "auto", "Colorize log output: auto, always or never")
	version    = "1.0.0"
)

func main() {
	flag.Parse()

	colorMode, err := logging.ParseColorMode(*colorFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logging.Configure(os.Stderr, colorMode)

	// Print banner
	printBanner()

	// Load configuration
	logConfig.Infof("Loading configuration from %s...", *configPath)
	cfg, err := config.Load(*configPath)
	if err != nil {
		logConfig.Fatalf("Failed to load config: %v", err)
	}
	logging.SetLevel(logging.ParseLevel(cfg.Monitoring.LogLevel))
	if *paperFlag {
		cfg.PaperTrading.Enabled = true
		if cfg.PaperTrading.StartingBalance <= 0 {
			cfg.PaperTrading.StartingBalance = 1000
		}
	}

	// Apply performance tuning
	applyPerformanceTuning(cfg)

	// Initialize components
	logSystem.Info("Initializing components...")

	// Metrics collector
	metricsCollector := metrics.NewCollector(cfg)

	// API client
	apiClient := api.NewClient(cfg)

	// Health check
	logAPI.Info("Checking API connectivity...")
	if err := apiClient.HealthCheck(); err != nil {
		logAPI.Warnf("API health check failed: %v", err)
	}

	// Strategy engine
	strategyEngine := strategy.NewEngine(cfg)

	var orderManager *order.Manager
	var paperTrader *paper.Trader

	// Order manager with submit function
	submitFunc := func(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
		responses := make([]*types.OrderResponse, len(requests))

		for i, req := range requests {
			startTime := time.Now()

			resp, err := apiClient.PlaceOrder(req)
			if err != nil {
				metricsCollector.RecordOrder(false)
				metricsCollector.RecordError()
				responses[i] = &types.OrderResponse{
					Success: false,
					Error:   err.Error(),
				}
				continue
			}

			// Record metrics
			latency := time.Since(startTime)
			metricsCollector.RecordLatency(latency)
			metricsCollector.RecordOrder(true)

			responses[i] = resp
		}

		return responses, nil
	}

	if cfg.PaperTrading.Enabled {
		paperTrader = paper.NewTrader(cfg.PaperTrading.StartingBalance)
		submitFunc = paperSubmitFunc(paperTrader, strategyEngine, &orderManager, metricsCollector)
		logPaper.Info(logging.Rainbow("=================================================="))
		logPaper.Info(logging.Rainbow("PAPER TRADING MODE - no real orders will be sent"))
		logPaper.Info(logging.Rainbow(fmt.Sprintf("Virtual balance: $%.2f", cfg.PaperTrading.StartingBalance)))
		logPaper.Info(logging.Rainbow("=================================================="))
	} else {
		logLive.Info("LIVE TRADING MODE - real orders will be sent with real funds")
	}

	orderManager = order.NewManager(cfg, submitFunc)

	// WebSocket client
	wsClient := websocket.NewClient(cfg)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start components
	logSystem.Info("Starting bot...")

	// Connect WebSocket
	if err := wsClient.Connect(); err != nil {
		logWS.Fatalf("Failed to connect WebSocket: %v", err)
	}
	logWS.Info("WebSocket connected")

	// Start main event loop
	go eventLoop(ctx, wsClient, strategyEngine, orderManager, metricsCollector, paperTrader)

	// Periodically report the paper portfolio
	if paperTrader != nil {
		go paperReportLoop(ctx, paperTrader, cfg.PaperTrading.ReportIntervalSec)
	}

	// Start monitoring loop
	go monitoringLoop(ctx, metricsCollector, cfg)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logSystem.Infof("Bot running (strategy: %s, markets: %d)", cfg.Strategy.Mode, len(cfg.Markets.TargetMarketIDs))
	logSystem.Info("Press Ctrl+C to stop")

	<-sigChan

	// Graceful shutdown
	logSystem.Info("\nShutting down gracefully...")
	cancel()

	// Close components
	wsClient.Close()
	orderManager.Close()
	strategyEngine.Close()

	// Print final stats
	stats := metricsCollector.GetStats()
	logStats.Info("\n=== Final Statistics ===")
	logStats.Infof("Messages received: %d", stats.MessagesReceived)
	logStats.Infof("Orders submitted: %d (success: %d, failed: %d)", stats.OrdersSubmitted, stats.OrdersSuccessful, stats.OrdersFailed)
	logStats.Infof("Trades executed: %d", stats.TradesExecuted)
	logStats.Infof("Total P&L: $%.2f", stats.TotalPnL)
	logStats.Infof("Uptime: %s", stats.Uptime)

	if paperTrader != nil {
		logPnL.Info("\n=== Paper Trading Results ===")
		logPaperSummary(paperTrader)
	}

	logSystem.Info("Shutdown complete")
}

// eventLoop is the main event processing loop
func eventLoop(ctx context.Context, wsClient *websocket.Client, strategyEngine *strategy.Engine, orderManager *order.Manager, metricsCollector *metrics.Collector, paperTrader *paper.Trader) {
	for {
		select {
		case <-ctx.Done():
			return

		case marketData := <-wsClient.Messages():
			// Record message
			metricsCollector.RecordMessage()

			// Value open paper positions at the latest price
			if paperTrader != nil {
				paperTrader.Mark(marketData.MarketID, marketData.Price)
			}

			// Process through strategy
			startTime := time.Now()
			signal, err := strategyEngine.ProcessMarketData(marketData)
			if err != nil {
				logStrat.Errorf("Strategy error: %v", err)
				metricsCollector.RecordError()
				continue
			}

			// If no signal, continue
			if signal == nil {
				continue
			}

			// Convert signal to order request
			orderReq := &types.OrderRequest{
				MarketID: signal.MarketID,
				Side:     signal.Action,
				Price:    signal.Price,
				Quantity: signal.Quantity,
			}

			// Handle close action (opposite side)
			if signal.Action == "close" {
				pos := strategyEngine.GetPosition(signal.MarketID)
				if pos != nil {
					if pos.Side == "long" {
						orderReq.Side = "sell"
					} else {
						orderReq.Side = "buy"
					}
				}
			}

			// Submit order
			if err := orderManager.SubmitOrder(orderReq); err != nil {
				logOrder.Errorf("Order submission failed: %v", err)
				continue
			}

			// Log signal
			totalLatency := time.Since(startTime)
			logSignal.Infof("[%s] %s %d @ %.4f - %s (latency: %dms)",
				signal.MarketID[:8],
				signal.Action,
				signal.Quantity,
				signal.Price,
				signal.Reason,
				totalLatency.Milliseconds())

		case err := <-wsClient.Errors():
			logWS.Errorf("WebSocket error: %v", err)
			metricsCollector.RecordError()
		}
	}
}

// paperSubmitFunc returns a submit function that simulates fills instead of calling the API.
// orderManager is a pointer because the manager is created after its submit function.
func paperSubmitFunc(trader *paper.Trader, strategyEngine *strategy.Engine, orderManager **order.Manager, metricsCollector *metrics.Collector) order.SubmitFunc {
	return func(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
		responses := make([]*types.OrderResponse, len(requests))

		for i, req := range requests {
			fill, err := trader.Execute(req)
			if err != nil {
				logOrder.Errorf("Paper order rejected: %v", err)
				metricsCollector.RecordOrder(false)
				responses[i] = &types.OrderResponse{Success: false, Error: err.Error()}
				continue
			}

			// Keep the strategy and risk tracking in sync with the simulated position
			strategyEngine.UpdatePosition(req.MarketID, fill.Side, fill.Quantity, fill.AvgPrice)
			(*orderManager).SetMarketExposure(req.MarketID, fill.Exposure)
			(*orderManager).RecordTrade(fill.Trade)
			metricsCollector.RecordOrder(true)
			metricsCollector.RecordTrade(fill.Trade.RealizedPnL)

			logFill.Infof("Paper %s %d @ %.4f in %s (realized P&L: $%.2f)",
				req.Side, req.Quantity, req.Price, req.MarketID, fill.Trade.RealizedPnL)

			responses[i] = &types.OrderResponse{
				Success: true,
				OrderID: fill.Trade.OrderID,
				Status:  "filled",
			}
		}

		return responses, nil
	}
}

// paperReportLoop logs the paper portfolio at a fixed interval
func paperReportLoop(ctx context.Context, trader *paper.Trader, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logPaperSummary(trader)
		}
	}
}

// logPaperSummary prints the current paper portfolio
func logPaperSummary(trader *paper.Trader) {
	s := trader.Summary()
	logPnL.Infof("Equity: $%.2f (%+.2f%%) | Cash: $%.2f | Realized: $%.2f | Unrealized: $%.2f | Trades: %d",
		s.Equity, s.TotalReturnPct, s.Cash, s.RealizedPnL, s.UnrealizedPnL, s.Trades)
	for _, p := range s.Positions {
		logPnL.Infof("  %s %s %d @ %.4f (mark %.4f, P&L $%.2f)",
			p.MarketID, p.Side, p.Quantity, p.AvgPrice, p.MarkPrice, p.UnrealizedPnL)
	}
}

// monitoringLoop periodically checks metrics and alerts
func monitoringLoop(ctx context.Context, metricsCollector *metrics.Collector, cfg *config.Config) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// Check thresholds
			alerts := metricsCollector.CheckThresholds()
			for _, alert := range alerts {
				logAlert.Warnf("%s", alert)
			}

			// Print stats
			stats := metricsCollector.GetStats()
			logStats.Debugf("msgs=%d orders=%d/%d trades=%d pnl=$%.2f lat=%dms err=%.2f%%",
				stats.MessagesReceived,
				stats.OrdersSuccessful,
				stats.OrdersSubmitted,
				stats.TradesExecuted,
				stats.DailyPnL,
				stats.AvgLatency.Milliseconds(),
				stats.ErrorRate)
		}
	}
}

// applyPerformanceTuning applies performance optimization settings
func applyPerformanceTuning(cfg *config.Config) {
	// Set GOGC if specified
	if cfg.Performance.GCPercent > 0 {
		debug.SetGCPercent(cfg.Performance.GCPercent)
		logSystem.Infof("Set GOGC=%d", cfg.Performance.GCPercent)
	}

	// Set GOMAXPROCS
	if cfg.Performance.WorkerCount > 0 {
		runtime.GOMAXPROCS(cfg.Performance.WorkerCount)
		logSystem.Infof("Set GOMAXPROCS=%d", cfg.Performance.WorkerCount)
	}

	// CPU affinity would require OS-specific code (not implemented in pure Go)
	if len(cfg.Performance.CPUCores) > 0 {
		logSystem.Infof("Note: CPU core pinning requires OS-specific implementation")
	}
}

// printBanner prints the startup banner
func printBanner() {
	banner := `
╔═══════════════════════════════════════════╗
║   Polymarket Trading Bot v%s           ║
║   Low-latency algorithmic trading         ║
╚═══════════════════════════════════════════╝
`
	fmt.Printf(banner, version)
}
