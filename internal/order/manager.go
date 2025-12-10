package order

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
	"golang.org/x/time/rate"
)

// Manager handles order submission with rate limiting and batching
type Manager struct {
	cfg             *config.OrdersConfig
	riskCfg         *config.RiskConfig
	limiter         *rate.Limiter
	outstandingOrders map[string]*types.Order
	mu              sync.RWMutex
	batch           []*types.OrderRequest
	batchMu         sync.Mutex
	batchTimer      *time.Timer
	ctx             context.Context
	cancel          context.CancelFunc
	submitFunc      SubmitFunc
	dailyLoss       float64
	dailyLossMu     sync.RWMutex
	marketExposure  map[string]float64
}

// SubmitFunc is a function that submits orders to the API
type SubmitFunc func([]*types.OrderRequest) ([]*types.OrderResponse, error)

// NewManager creates a new order manager
func NewManager(cfg *config.Config, submitFunc SubmitFunc) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Create rate limiter (orders per second)
	limiter := rate.NewLimiter(rate.Limit(cfg.Orders.MaxOrdersPerSecond), cfg.Orders.MaxOrdersPerSecond)

	m := &Manager{
		cfg:               &cfg.Orders,
		riskCfg:           &cfg.Risk,
		limiter:           limiter,
		outstandingOrders: make(map[string]*types.Order),
		batch:             make([]*types.OrderRequest, 0, cfg.Orders.BatchSize),
		ctx:               ctx,
		cancel:            cancel,
		submitFunc:        submitFunc,
		marketExposure:    make(map[string]float64),
	}

	// Start batch processor if batching enabled
	if cfg.Orders.BatchSize > 1 {
		go m.batchProcessor()
	}

	// Reset daily loss counter at midnight
	go m.dailyLossReset()

	return m
}

// SubmitOrder queues an order for submission
func (m *Manager) SubmitOrder(req *types.OrderRequest) error {
	// Validate request
	if err := m.validateOrder(req); err != nil {
		return fmt.Errorf("order validation failed: %w", err)
	}

	// Check rate limit
	if err := m.limiter.Wait(m.ctx); err != nil {
		return fmt.Errorf("rate limit error: %w", err)
	}

	// Check outstanding orders limit
	if err := m.checkOutstandingLimit(); err != nil {
		return err
	}

	// Check risk limits
	if err := m.checkRiskLimits(req); err != nil {
		return err
	}

	// If batching enabled, add to batch
	if m.cfg.BatchSize > 1 {
		return m.addToBatch(req)
	}

	// Otherwise submit immediately
	return m.submitImmediate([]*types.OrderRequest{req})
}

// validateOrder validates order parameters
func (m *Manager) validateOrder(req *types.OrderRequest) error {
	if req.MarketID == "" {
		return fmt.Errorf("market_id is required")
	}
	if req.Side != "buy" && req.Side != "sell" {
		return fmt.Errorf("invalid side: must be 'buy' or 'sell'")
	}
	if req.Price <= 0 {
		return fmt.Errorf("price must be positive")
	}
	if req.Quantity <= 0 {
		return fmt.Errorf("quantity must be positive")
	}
	if req.Type == "" {
		req.Type = m.cfg.DefaultOrderType
	}

	return nil
}

// checkOutstandingLimit checks if we can submit more orders
func (m *Manager) checkOutstandingLimit() error {
	m.mu.RLock()
	count := len(m.outstandingOrders)
	m.mu.RUnlock()

	if count >= m.cfg.MaxOutstanding {
		return fmt.Errorf("max outstanding orders reached: %d/%d", count, m.cfg.MaxOutstanding)
	}

	return nil
}

// checkRiskLimits validates risk management rules
func (m *Manager) checkRiskLimits(req *types.OrderRequest) error {
	// Check daily loss limit
	m.dailyLossMu.RLock()
	currentLoss := m.dailyLoss
	m.dailyLossMu.RUnlock()

	if m.riskCfg.AutoHaltOnLoss && currentLoss >= m.riskCfg.MaxDailyLoss {
		return fmt.Errorf("daily loss limit reached: %.2f/%.2f", currentLoss, m.riskCfg.MaxDailyLoss)
	}

	// Check market exposure
	m.mu.RLock()
	exposure := m.marketExposure[req.MarketID]
	m.mu.RUnlock()

	orderValue := req.Price * float64(req.Quantity)
	if exposure+orderValue > m.riskCfg.MaxExposurePerMarket {
		return fmt.Errorf("order would exceed max exposure for market %s: current=%.2f, order=%.2f, max=%.2f",
			req.MarketID, exposure, orderValue, m.riskCfg.MaxExposurePerMarket)
	}

	return nil
}

// addToBatch adds an order to the batch queue
func (m *Manager) addToBatch(req *types.OrderRequest) error {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()

	m.batch = append(m.batch, req)

	// If batch full, submit immediately
	if len(m.batch) >= m.cfg.BatchSize {
		batch := m.batch
		m.batch = make([]*types.OrderRequest, 0, m.cfg.BatchSize)
		go m.submitImmediate(batch)
		return nil
	}

	// Otherwise set/reset timer
	if m.batchTimer != nil {
		m.batchTimer.Stop()
	}

	m.batchTimer = time.AfterFunc(
		time.Duration(m.cfg.BatchIntervalMS)*time.Millisecond,
		func() {
			m.flushBatch()
		},
	)

	return nil
}

// flushBatch submits the current batch
func (m *Manager) flushBatch() {
	m.batchMu.Lock()
	if len(m.batch) == 0 {
		m.batchMu.Unlock()
		return
	}

	batch := m.batch
	m.batch = make([]*types.OrderRequest, 0, m.cfg.BatchSize)
	m.batchMu.Unlock()

	m.submitImmediate(batch)
}

// batchProcessor periodically flushes the batch
func (m *Manager) batchProcessor() {
	ticker := time.NewTicker(time.Duration(m.cfg.BatchIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.flushBatch()
		}
	}
}

// submitImmediate submits orders immediately with retry logic
func (m *Manager) submitImmediate(requests []*types.OrderRequest) error {
	var lastErr error

	for attempt := 0; attempt <= m.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(m.cfg.RetryDelayMS) * time.Millisecond)
		}

		responses, err := m.submitFunc(requests)
		if err != nil {
			lastErr = err
			continue
		}

		// Process responses
		m.processResponses(requests, responses)
		return nil
	}

	return fmt.Errorf("failed after %d retries: %w", m.cfg.MaxRetries, lastErr)
}

// processResponses handles order submission responses
func (m *Manager) processResponses(requests []*types.OrderRequest, responses []*types.OrderResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, resp := range responses {
		if resp.Success {
			// Track outstanding order
			order := &types.Order{
				ID:        resp.OrderID,
				MarketID:  requests[i].MarketID,
				Side:      requests[i].Side,
				Type:      requests[i].Type,
				Price:     requests[i].Price,
				Quantity:  requests[i].Quantity,
				Status:    "open",
				CreatedAt: time.Now(),
			}
			m.outstandingOrders[order.ID] = order

			// Update market exposure
			exposure := order.Price * float64(order.Quantity)
			m.marketExposure[order.MarketID] += exposure
		}
	}
}

// UpdateOrderStatus updates the status of an order
func (m *Manager) UpdateOrderStatus(orderID string, status string, filledQty int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	order, exists := m.outstandingOrders[orderID]
	if !exists {
		return
	}

	order.Status = status
	order.FilledQty = filledQty
	order.UpdatedAt = time.Now()

	// Remove from outstanding if filled or cancelled
	if status == "filled" || status == "cancelled" {
		delete(m.outstandingOrders, orderID)

		// Update market exposure
		filledValue := order.Price * float64(filledQty)
		m.marketExposure[order.MarketID] -= filledValue
	}
}

// RecordTrade records a completed trade and updates P&L tracking
func (m *Manager) RecordTrade(trade *types.Trade) {
	if trade.RealizedPnL < 0 {
		m.dailyLossMu.Lock()
		m.dailyLoss += -trade.RealizedPnL
		m.dailyLossMu.Unlock()
	}
}

// GetOutstandingOrders returns all outstanding orders
func (m *Manager) GetOutstandingOrders() []*types.Order {
	m.mu.RLock()
	defer m.mu.RUnlock()

	orders := make([]*types.Order, 0, len(m.outstandingOrders))
	for _, order := range m.outstandingOrders {
		orderCopy := *order
		orders = append(orders, &orderCopy)
	}

	return orders
}

// GetDailyLoss returns current daily loss
func (m *Manager) GetDailyLoss() float64 {
	m.dailyLossMu.RLock()
	defer m.dailyLossMu.RUnlock()
	return m.dailyLoss
}

// dailyLossReset resets the daily loss counter at midnight
func (m *Manager) dailyLossReset() {
	for {
		now := time.Now()
		tomorrow := now.Add(24 * time.Hour)
		midnight := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, tomorrow.Location())
		duration := midnight.Sub(now)

		select {
		case <-m.ctx.Done():
			return
		case <-time.After(duration):
			m.dailyLossMu.Lock()
			m.dailyLoss = 0
			m.dailyLossMu.Unlock()
		}
	}
}

// Close shuts down the order manager
func (m *Manager) Close() {
	m.cancel()

	// Flush any remaining batch
	if m.cfg.BatchSize > 1 {
		m.flushBatch()
	}
}
