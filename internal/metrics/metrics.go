package metrics

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
)

// Collector tracks bot performance metrics
type Collector struct {
	cfg *config.MonitoringConfig

	// Counters
	messagesReceived   atomic.Uint64
	ordersSubmitted    atomic.Uint64
	ordersSuccessful   atomic.Uint64
	ordersFailed       atomic.Uint64
	tradesExecuted     atomic.Uint64

	// Latency tracking
	latencyMu        sync.RWMutex
	latencyHistogram []time.Duration
	maxLatency       time.Duration
	avgLatency       time.Duration

	// Error tracking
	errorsMu       sync.RWMutex
	errorCount     uint64
	totalOperations uint64

	// P&L tracking
	pnlMu      sync.RWMutex
	totalPnL   float64
	dailyPnL   float64

	// Start time
	startTime time.Time
}

// NewCollector creates a new metrics collector
func NewCollector(cfg *config.Config) *Collector {
	c := &Collector{
		cfg:              &cfg.Monitoring,
		latencyHistogram: make([]time.Duration, 0, 10000),
		startTime:        time.Now(),
	}

	// Start metrics server if enabled
	if cfg.Monitoring.Enabled && cfg.Monitoring.MetricsPort > 0 {
		go c.startMetricsServer(cfg.Monitoring.MetricsPort)
	}

	// Start periodic metric calculation
	go c.calculateMetrics()

	return c
}

// RecordMessage increments message counter
func (c *Collector) RecordMessage() {
	c.messagesReceived.Add(1)
}

// RecordOrder records an order submission
func (c *Collector) RecordOrder(success bool) {
	c.ordersSubmitted.Add(1)
	c.totalOperations++

	if success {
		c.ordersSuccessful.Add(1)
	} else {
		c.ordersFailed.Add(1)
		c.errorsMu.Lock()
		c.errorCount++
		c.errorsMu.Unlock()
	}
}

// RecordTrade records a completed trade
func (c *Collector) RecordTrade(pnl float64) {
	c.tradesExecuted.Add(1)

	c.pnlMu.Lock()
	c.totalPnL += pnl
	c.dailyPnL += pnl
	c.pnlMu.Unlock()
}

// RecordLatency tracks end-to-end latency
func (c *Collector) RecordLatency(duration time.Duration) {
	if !c.cfg.TrackLatency {
		return
	}

	c.latencyMu.Lock()
	defer c.latencyMu.Unlock()

	c.latencyHistogram = append(c.latencyHistogram, duration)

	// Keep only last 10000 measurements
	if len(c.latencyHistogram) > 10000 {
		c.latencyHistogram = c.latencyHistogram[1:]
	}

	if duration > c.maxLatency {
		c.maxLatency = duration
	}
}

// RecordError records an error occurrence
func (c *Collector) RecordError() {
	c.errorsMu.Lock()
	c.errorCount++
	c.totalOperations++
	c.errorsMu.Unlock()
}

// GetStats returns current metrics snapshot
func (c *Collector) GetStats() *Stats {
	c.latencyMu.RLock()
	avgLat := c.avgLatency
	maxLat := c.maxLatency
	c.latencyMu.RUnlock()

	c.pnlMu.RLock()
	totalPnL := c.totalPnL
	dailyPnL := c.dailyPnL
	c.pnlMu.RUnlock()

	c.errorsMu.RLock()
	errCount := c.errorCount
	totalOps := c.totalOperations
	c.errorsMu.RUnlock()

	var errorRate float64
	if totalOps > 0 {
		errorRate = float64(errCount) / float64(totalOps) * 100
	}

	return &Stats{
		MessagesReceived: c.messagesReceived.Load(),
		OrdersSubmitted:  c.ordersSubmitted.Load(),
		OrdersSuccessful: c.ordersSuccessful.Load(),
		OrdersFailed:     c.ordersFailed.Load(),
		TradesExecuted:   c.tradesExecuted.Load(),
		AvgLatency:       avgLat,
		MaxLatency:       maxLat,
		ErrorCount:       errCount,
		ErrorRate:        errorRate,
		TotalPnL:         totalPnL,
		DailyPnL:         dailyPnL,
		Uptime:           time.Since(c.startTime),
	}
}

// CheckThresholds checks if any alert thresholds are exceeded
func (c *Collector) CheckThresholds() []string {
	var alerts []string

	// Check latency
	if c.cfg.MaxLatencyMS > 0 {
		c.latencyMu.RLock()
		avgLatMS := c.avgLatency.Milliseconds()
		c.latencyMu.RUnlock()

		if avgLatMS > int64(c.cfg.MaxLatencyMS) {
			alerts = append(alerts, fmt.Sprintf("Avg latency %dms exceeds threshold %dms", avgLatMS, c.cfg.MaxLatencyMS))
		}
	}

	// Check error rate
	if c.cfg.MaxErrorRatePct > 0 {
		stats := c.GetStats()
		if stats.ErrorRate > c.cfg.MaxErrorRatePct {
			alerts = append(alerts, fmt.Sprintf("Error rate %.2f%% exceeds threshold %.2f%%", stats.ErrorRate, c.cfg.MaxErrorRatePct))
		}
	}

	return alerts
}

// calculateMetrics periodically calculates derived metrics
func (c *Collector) calculateMetrics() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.latencyMu.Lock()
		if len(c.latencyHistogram) > 0 {
			var sum time.Duration
			for _, lat := range c.latencyHistogram {
				sum += lat
			}
			c.avgLatency = sum / time.Duration(len(c.latencyHistogram))
		}
		c.latencyMu.Unlock()
	}
}

// ResetDailyPnL resets the daily P&L counter
func (c *Collector) ResetDailyPnL() {
	c.pnlMu.Lock()
	c.dailyPnL = 0
	c.pnlMu.Unlock()
}

// startMetricsServer starts HTTP server for metrics exposition
func (c *Collector) startMetricsServer(port int) {
	mux := http.NewServeMux()

	// Prometheus-style metrics endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		stats := c.GetStats()

		fmt.Fprintf(w, "# HELP polymarket_messages_received_total Total messages received from WebSocket\n")
		fmt.Fprintf(w, "# TYPE polymarket_messages_received_total counter\n")
		fmt.Fprintf(w, "polymarket_messages_received_total %d\n\n", stats.MessagesReceived)

		fmt.Fprintf(w, "# HELP polymarket_orders_submitted_total Total orders submitted\n")
		fmt.Fprintf(w, "# TYPE polymarket_orders_submitted_total counter\n")
		fmt.Fprintf(w, "polymarket_orders_submitted_total %d\n\n", stats.OrdersSubmitted)

		fmt.Fprintf(w, "# HELP polymarket_orders_successful_total Total successful orders\n")
		fmt.Fprintf(w, "# TYPE polymarket_orders_successful_total counter\n")
		fmt.Fprintf(w, "polymarket_orders_successful_total %d\n\n", stats.OrdersSuccessful)

		fmt.Fprintf(w, "# HELP polymarket_orders_failed_total Total failed orders\n")
		fmt.Fprintf(w, "# TYPE polymarket_orders_failed_total counter\n")
		fmt.Fprintf(w, "polymarket_orders_failed_total %d\n\n", stats.OrdersFailed)

		fmt.Fprintf(w, "# HELP polymarket_trades_executed_total Total trades executed\n")
		fmt.Fprintf(w, "# TYPE polymarket_trades_executed_total counter\n")
		fmt.Fprintf(w, "polymarket_trades_executed_total %d\n\n", stats.TradesExecuted)

		fmt.Fprintf(w, "# HELP polymarket_latency_avg_ms Average end-to-end latency in milliseconds\n")
		fmt.Fprintf(w, "# TYPE polymarket_latency_avg_ms gauge\n")
		fmt.Fprintf(w, "polymarket_latency_avg_ms %d\n\n", stats.AvgLatency.Milliseconds())

		fmt.Fprintf(w, "# HELP polymarket_latency_max_ms Maximum latency in milliseconds\n")
		fmt.Fprintf(w, "# TYPE polymarket_latency_max_ms gauge\n")
		fmt.Fprintf(w, "polymarket_latency_max_ms %d\n\n", stats.MaxLatency.Milliseconds())

		fmt.Fprintf(w, "# HELP polymarket_error_rate_pct Error rate percentage\n")
		fmt.Fprintf(w, "# TYPE polymarket_error_rate_pct gauge\n")
		fmt.Fprintf(w, "polymarket_error_rate_pct %.2f\n\n", stats.ErrorRate)

		fmt.Fprintf(w, "# HELP polymarket_pnl_total Total P&L\n")
		fmt.Fprintf(w, "# TYPE polymarket_pnl_total gauge\n")
		fmt.Fprintf(w, "polymarket_pnl_total %.2f\n\n", stats.TotalPnL)

		fmt.Fprintf(w, "# HELP polymarket_pnl_daily Daily P&L\n")
		fmt.Fprintf(w, "# TYPE polymarket_pnl_daily gauge\n")
		fmt.Fprintf(w, "polymarket_pnl_daily %.2f\n\n", stats.DailyPnL)

		fmt.Fprintf(w, "# HELP polymarket_uptime_seconds Bot uptime in seconds\n")
		fmt.Fprintf(w, "# TYPE polymarket_uptime_seconds gauge\n")
		fmt.Fprintf(w, "polymarket_uptime_seconds %.0f\n", stats.Uptime.Seconds())
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting metrics server on %s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Metrics server error: %v", err)
	}
}

// Stats represents a snapshot of metrics
type Stats struct {
	MessagesReceived uint64
	OrdersSubmitted  uint64
	OrdersSuccessful uint64
	OrdersFailed     uint64
	TradesExecuted   uint64
	AvgLatency       time.Duration
	MaxLatency       time.Duration
	ErrorCount       uint64
	ErrorRate        float64
	TotalPnL         float64
	DailyPnL         float64
	Uptime           time.Duration
}
