package types

import "time"

// MarketData represents real-time market data from WebSocket
type MarketData struct {
	MarketID    string    `json:"market_id"`
	Price       float64   `json:"price"`
	BidPrice    float64   `json:"bid_price"`
	AskPrice    float64   `json:"ask_price"`
	Volume      float64   `json:"volume"`
	Liquidity   float64   `json:"liquidity"`
	Timestamp   time.Time `json:"timestamp"`
	LastUpdated time.Time `json:"last_updated"`
}

// Order represents a trading order
type Order struct {
	ID            string    `json:"id"`
	MarketID      string    `json:"market_id"`
	Side          string    `json:"side"` // "buy" or "sell"
	Type          string    `json:"type"` // "GTC", "IOC", "FOK"
	Price         float64   `json:"price"`
	Quantity      int       `json:"quantity"`
	FilledQty     int       `json:"filled_qty"`
	Status        string    `json:"status"` // "pending", "open", "filled", "cancelled"
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Position represents a current market position
type Position struct {
	MarketID      string    `json:"market_id"`
	Side          string    `json:"side"` // "long" or "short"
	Quantity      int       `json:"quantity"`
	AvgEntryPrice float64   `json:"avg_entry_price"`
	CurrentPrice  float64   `json:"current_price"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
	OpenedAt      time.Time `json:"opened_at"`
	LastUpdated   time.Time `json:"last_updated"`
}

// Signal represents a trading signal from the strategy
type Signal struct {
	MarketID  string    `json:"market_id"`
	Action    string    `json:"action"` // "buy", "sell", "hold", "close"
	Price     float64   `json:"price"`
	Quantity  int       `json:"quantity"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// Trade represents an executed trade
type Trade struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	MarketID    string    `json:"market_id"`
	Side        string    `json:"side"`
	Price       float64   `json:"price"`
	Quantity    int       `json:"quantity"`
	Fee         float64   `json:"fee"`
	RealizedPnL float64   `json:"realized_pnl"`
	ExecutedAt  time.Time `json:"executed_at"`
}

// OrderRequest represents a request to place an order
type OrderRequest struct {
	MarketID string  `json:"market_id"`
	Side     string  `json:"side"`
	Type     string  `json:"type"`
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}

// OrderResponse represents the API response for an order
type OrderResponse struct {
	Success bool   `json:"success"`
	OrderID string `json:"order_id"`
	Error   string `json:"error,omitempty"`
}

// WebSocketMessage represents an incoming WebSocket message
type WebSocketMessage struct {
	Type      string      `json:"type"`
	Channel   string      `json:"channel"`
	Data      interface{} `json:"data"`
	Timestamp time.Time   `json:"timestamp"`
}

// LatencyMetrics tracks latency at various stages
type LatencyMetrics struct {
	WSReceive     time.Duration
	StrategyCalc  time.Duration
	OrderSubmit   time.Duration
	OrderAck      time.Duration
	TotalLatency  time.Duration
}
