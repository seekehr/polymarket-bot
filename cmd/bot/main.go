package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/seeker/polymarket-bot/internal/api"
	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/internal/metrics"
	"github.com/seeker/polymarket-bot/internal/order"
	"github.com/seeker/polymarket-bot/internal/strategy"
	"github.com/seeker/polymarket-bot/internal/websocket"
	"github.com/seeker/polymarket-bot/pkg/types"
)

var (
	configPath = flag.String("config", "config.yml", "Path to configuration file")
	version    = "1.0.0"
)

func main() {
	flag.Parse()

	// Print banner
	printBanner()

	// Load configuration
	log.Printf("Loading configuration from %s...", *configPath)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Apply performance tuning
	applyPerformanceTuning(cfg)

	// Initialize components
	log.Println("Initializing components...")

	// Metrics collector
	metricsCollector := metrics.NewCollector(cfg)

	// API client
	apiClient := api.NewClient(cfg)

	// Health check
	log.Println("Checking API connectivity...")
	if err := apiClient.HealthCheck(); err != nil {
		log.Printf("Warning: API health check failed: %v", err)
	}

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

	orderManager := order.NewManager(cfg, submitFunc)

	// Strategy engine
	strategyEngine := strategy.NewEngine(cfg)

	// WebSocket client
	wsClient := websocket.NewClient(cfg)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start components
	log.Println("Starting bot...")

	// Connect WebSocket
	if err := wsClient.Connect(); err != nil {
		log.Fatalf("Failed to connect WebSocket: %v", err)
	}
	log.Println("WebSocket connected")

	// Start main event loop
	go eventLoop(ctx, wsClient, strategyEngine, orderManager, metricsCollector)

	// Start monitoring loop
	go monitoringLoop(ctx, metricsCollector, cfg)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	log.Printf("Bot running (strategy: %s, markets: %d)", cfg.Strategy.Mode, len(cfg.Markets.TargetMarketIDs))
	log.Println("Press Ctrl+C to stop")

	<-sigChan

	// Graceful shutdown
	log.Println("\nShutting down gracefully...")
	cancel()

	// Close components
	wsClient.Close()
	orderManager.Close()
	strategyEngine.Close()

	// Print final stats
	stats := metricsCollector.GetStats()
	log.Println("\n=== Final Statistics ===")
	log.Printf("Messages received: %d", stats.MessagesReceived)
	log.Printf("Orders submitted: %d (success: %d, failed: %d)", stats.OrdersSubmitted, stats.OrdersSuccessful, stats.OrdersFailed)
	log.Printf("Trades executed: %d", stats.TradesExecuted)
	log.Printf("Total P&L: $%.2f", stats.TotalPnL)
	log.Printf("Uptime: %s", stats.Uptime)

	log.Println("Shutdown complete")
}

// eventLoop is the main event processing loop
func eventLoop(ctx context.Context, wsClient *websocket.Client, strategyEngine *strategy.Engine, orderManager *order.Manager, metricsCollector *metrics.Collector) {
	for {
		select {
		case <-ctx.Done():
			return

		case marketData := <-wsClient.Messages():
			// Record message
			metricsCollector.RecordMessage()

			// Process through strategy
			startTime := time.Now()
			signal, err := strategyEngine.ProcessMarketData(marketData)
			if err != nil {
				log.Printf("Strategy error: %v", err)
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
				log.Printf("Order submission failed: %v", err)
				continue
			}

			// Log signal
			totalLatency := time.Since(startTime)
			log.Printf("[%s] %s %d @ %.4f - %s (latency: %dms)",
				signal.MarketID[:8],
				signal.Action,
				signal.Quantity,
				signal.Price,
				signal.Reason,
				totalLatency.Milliseconds())

		case err := <-wsClient.Errors():
			log.Printf("WebSocket error: %v", err)
			metricsCollector.RecordError()
		}
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
				log.Printf("ALERT: %s", alert)
			}

			// Print stats
			stats := metricsCollector.GetStats()
			if cfg.Monitoring.LogLevel == "debug" {
				log.Printf("Stats: msgs=%d orders=%d/%d trades=%d pnl=$%.2f lat=%dms err=%.2f%%",
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
}

// applyPerformanceTuning applies performance optimization settings
func applyPerformanceTuning(cfg *config.Config) {
	// Set GOGC if specified
	if cfg.Performance.GCPercent > 0 {
		debug.SetGCPercent(cfg.Performance.GCPercent)
		log.Printf("Set GOGC=%d", cfg.Performance.GCPercent)
	}

	// Set GOMAXPROCS
	if cfg.Performance.WorkerCount > 0 {
		runtime.GOMAXPROCS(cfg.Performance.WorkerCount)
		log.Printf("Set GOMAXPROCS=%d", cfg.Performance.WorkerCount)
	}

	// CPU affinity would require OS-specific code (not implemented in pure Go)
	if len(cfg.Performance.CPUCores) > 0 {
		log.Printf("Note: CPU core pinning requires OS-specific implementation")
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
