package strategy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

// Engine manages trading strategy logic
type Engine struct {
	cfg          *config.StrategyConfig
	positions    map[string]*types.Position
	signals      chan *types.Signal
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	marketPrices map[string]float64 // Last known price for each market
}

// NewEngine creates a new strategy engine
func NewEngine(cfg *config.Config) *Engine {
	ctx, cancel := context.WithCancel(context.Background())

	return &Engine{
		cfg:          &cfg.Strategy,
		positions:    make(map[string]*types.Position),
		signals:      make(chan *types.Signal, 1000),
		ctx:          ctx,
		cancel:       cancel,
		marketPrices: make(map[string]float64),
	}
}

// ProcessMarketData analyzes market data and generates trading signals
func (e *Engine) ProcessMarketData(data *types.MarketData) (*types.Signal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Update last known price
	e.marketPrices[data.MarketID] = data.Price

	// Get current position for this market
	position := e.positions[data.MarketID]

	// Generate signal based on strategy mode
	var signal *types.Signal
	var err error

	switch e.cfg.Mode {
	case "threshold":
		signal, err = e.thresholdStrategy(data, position)
	case "market_making":
		signal, err = e.marketMakingStrategy(data, position)
	case "momentum":
		signal, err = e.momentumStrategy(data, position)
	default:
		return nil, fmt.Errorf("unknown strategy mode: %s", e.cfg.Mode)
	}

	if err != nil {
		return nil, err
	}

	// Send signal to channel if not nil
	if signal != nil {
		select {
		case e.signals <- signal:
		default:
			return signal, fmt.Errorf("signal channel full")
		}
	}

	return signal, nil
}

// thresholdStrategy implements buy-below/sell-above logic
func (e *Engine) thresholdStrategy(data *types.MarketData, position *types.Position) (*types.Signal, error) {
	// No position: look for entry
	if position == nil || position.Quantity == 0 {
		// Buy signal if price below threshold
		if data.Price < e.cfg.BuyBelow {
			return &types.Signal{
				MarketID:  data.MarketID,
				Action:    "buy",
				Price:     data.AskPrice, // Buy at ask
				Quantity:  e.cfg.Quantity,
				Reason:    fmt.Sprintf("Price %.4f below buy threshold %.4f", data.Price, e.cfg.BuyBelow),
				Timestamp: time.Now(),
			}, nil
		}

		// Sell signal if price above threshold (for short positions)
		if data.Price > e.cfg.SellAbove {
			return &types.Signal{
				MarketID:  data.MarketID,
				Action:    "sell",
				Price:     data.BidPrice, // Sell at bid
				Quantity:  e.cfg.Quantity,
				Reason:    fmt.Sprintf("Price %.4f above sell threshold %.4f", data.Price, e.cfg.SellAbove),
				Timestamp: time.Now(),
			}, nil
		}

		return nil, nil
	}

	// Have position: look for exit
	if position.Side == "long" {
		// Close long position if price above sell threshold
		if data.Price > e.cfg.SellAbove {
			return &types.Signal{
				MarketID:  data.MarketID,
				Action:    "close",
				Price:     data.BidPrice,
				Quantity:  position.Quantity,
				Reason:    fmt.Sprintf("Taking profit: price %.4f above sell threshold %.4f", data.Price, e.cfg.SellAbove),
				Timestamp: time.Now(),
			}, nil
		}
	}

	if position.Side == "short" {
		// Close short position if price below buy threshold
		if data.Price < e.cfg.BuyBelow {
			return &types.Signal{
				MarketID:  data.MarketID,
				Action:    "close",
				Price:     data.AskPrice,
				Quantity:  position.Quantity,
				Reason:    fmt.Sprintf("Taking profit: price %.4f below buy threshold %.4f", data.Price, e.cfg.BuyBelow),
				Timestamp: time.Now(),
			}, nil
		}
	}

	return nil, nil
}

// marketMakingStrategy provides liquidity on both sides
func (e *Engine) marketMakingStrategy(data *types.MarketData, position *types.Position) (*types.Signal, error) {
	// Simple market making: quote both sides with a spread
	spread := 0.02 // 2% spread

	bidPrice := data.Price * (1 - spread/2)
	// askPrice := data.Price * (1 + spread/2) // For future sell-side implementation

	// Check if we should adjust position
	if position == nil || position.Quantity < e.cfg.MaxPositionSize {
		// Post buy order
		return &types.Signal{
			MarketID:  data.MarketID,
			Action:    "buy",
			Price:     bidPrice,
			Quantity:  e.cfg.Quantity,
			Reason:    "Market making: posting bid",
			Timestamp: time.Now(),
		}, nil
	}

	return nil, nil
}

// momentumStrategy follows price momentum
func (e *Engine) momentumStrategy(data *types.MarketData, position *types.Position) (*types.Signal, error) {
	// Simple momentum: buy if price increasing, sell if decreasing
	// This would need historical data for proper implementation
	// Placeholder logic
	return nil, nil
}

// UpdatePosition updates the internal position tracking
func (e *Engine) UpdatePosition(marketID string, side string, quantity int, avgPrice float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if quantity == 0 {
		delete(e.positions, marketID)
		return
	}

	e.positions[marketID] = &types.Position{
		MarketID:      marketID,
		Side:          side,
		Quantity:      quantity,
		AvgEntryPrice: avgPrice,
		CurrentPrice:  e.marketPrices[marketID],
		OpenedAt:      time.Now(),
		LastUpdated:   time.Now(),
	}
}

// GetPosition returns the current position for a market
func (e *Engine) GetPosition(marketID string) *types.Position {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.positions[marketID]
}

// GetAllPositions returns all current positions
func (e *Engine) GetAllPositions() map[string]*types.Position {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Return a copy to avoid race conditions
	positions := make(map[string]*types.Position)
	for k, v := range e.positions {
		posCopy := *v
		positions[k] = &posCopy
	}

	return positions
}

// CalculateTotalExposure returns total position size across all markets
func (e *Engine) CalculateTotalExposure() int {
	e.mu.RLock()
	defer e.mu.RUnlock()

	total := 0
	for _, pos := range e.positions {
		total += pos.Quantity
	}

	return total
}

// CheckPositionLimits validates if a new trade would exceed limits
func (e *Engine) CheckPositionLimits(marketID string, quantity int) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Check total position size
	totalExposure := e.CalculateTotalExposure()
	if totalExposure+quantity > e.cfg.MaxPositionSize {
		return fmt.Errorf("trade would exceed max position size: current=%d, new=%d, max=%d",
			totalExposure, quantity, e.cfg.MaxPositionSize)
	}

	return nil
}

// Signals returns the channel for receiving trading signals
func (e *Engine) Signals() <-chan *types.Signal {
	return e.signals
}

// Close shuts down the strategy engine
func (e *Engine) Close() {
	e.cancel()
	close(e.signals)
}
