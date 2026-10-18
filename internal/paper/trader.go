package paper

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/pkg/types"
)

// Trader simulates order execution against a virtual balance.
// Orders fill immediately and in full at the requested price; nothing is sent to Polymarket.
//
// Polymarket outcome shares pay $1 or $0, so a short of Q shares at price P is
// modelled as buying Q of the opposite outcome at (1 - P): it locks (1 - P) * Q of cash.
type Trader struct {
	mu          sync.Mutex
	startingBal float64
	cash        float64
	realizedPnL float64
	positions   map[string]*holding
	lastPrices  map[string]float64
	trades      []*types.Trade
	nextID      int
}

// holding is a net position: Qty > 0 is long, Qty < 0 is short
type holding struct {
	Qty      int
	AvgPrice float64
}

// Fill describes the result of a simulated execution
type Fill struct {
	Trade *types.Trade
	// Resulting position in the market after the fill
	Side     string // "long", "short" or "" if flat
	Quantity int
	AvgPrice float64
	// Exposure is the USD value of the resulting position at its entry price
	Exposure float64
}

// Summary is a snapshot of the paper portfolio
type Summary struct {
	StartingBalance float64
	Cash            float64
	PositionsValue  float64
	Equity          float64
	RealizedPnL     float64
	UnrealizedPnL   float64
	TotalReturnPct  float64
	Trades          int
	Positions       []PositionSummary
}

// PositionSummary describes one open paper position
type PositionSummary struct {
	MarketID      string
	Side          string
	Quantity      int
	AvgPrice      float64
	MarkPrice     float64
	UnrealizedPnL float64
	Exposure      float64 // USD locked at entry, as reported in Fill.Exposure
}

// NewTrader creates a paper trader with the given virtual balance (USD)
func NewTrader(startingBalance float64) *Trader {
	return &Trader{
		startingBal: startingBalance,
		cash:        startingBalance,
		positions:   make(map[string]*holding),
		lastPrices:  make(map[string]float64),
	}
}

// Execute simulates filling an order. It returns an error if the order is
// invalid or the virtual balance cannot cover it.
func (t *Trader) Execute(req *types.OrderRequest) (*Fill, error) {
	if req.Price <= 0 || req.Price >= 1 {
		return nil, fmt.Errorf("paper: price %.4f outside (0, 1)", req.Price)
	}
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("paper: quantity must be positive")
	}
	if req.Side != "buy" && req.Side != "sell" {
		return nil, fmt.Errorf("paper: invalid side %q", req.Side)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	h := t.positions[req.MarketID]
	if h == nil {
		h = &holding{}
	}
	p := req.Price
	q := req.Quantity

	// Split the order into the part that closes an existing position and the part that opens a new one
	closeQty := 0
	if req.Side == "buy" && h.Qty < 0 {
		closeQty = min(q, -h.Qty)
	} else if req.Side == "sell" && h.Qty > 0 {
		closeQty = min(q, h.Qty)
	}
	openQty := q - closeQty

	// Cash released by closing, and cash needed to open
	var released, pnl, required float64
	if req.Side == "buy" {
		released = (1 - p) * float64(closeQty)
		pnl = (h.AvgPrice - p) * float64(closeQty)
		required = p * float64(openQty)
	} else {
		released = p * float64(closeQty)
		pnl = (p - h.AvgPrice) * float64(closeQty)
		required = (1 - p) * float64(openQty)
	}

	if required > t.cash+released+1e-9 {
		return nil, fmt.Errorf("paper: insufficient virtual balance: need $%.2f, have $%.2f",
			required, t.cash+released)
	}

	t.cash += released - required
	t.realizedPnL += pnl

	// Update the net position
	if closeQty > 0 {
		if h.Qty > 0 {
			h.Qty -= closeQty
		} else {
			h.Qty += closeQty
		}
	}
	if openQty > 0 {
		// After closing, the position is flat (or was already on this side)
		signed := openQty
		if req.Side == "sell" {
			signed = -openQty
		}
		absOld := math.Abs(float64(h.Qty))
		h.AvgPrice = (h.AvgPrice*absOld + p*float64(openQty)) / (absOld + float64(openQty))
		h.Qty += signed
	}

	if h.Qty == 0 {
		delete(t.positions, req.MarketID)
	} else {
		t.positions[req.MarketID] = h
	}
	t.lastPrices[req.MarketID] = p

	t.nextID++
	now := time.Now()
	trade := &types.Trade{
		ID:          fmt.Sprintf("paper-trade-%d", t.nextID),
		OrderID:     fmt.Sprintf("paper-order-%d", t.nextID),
		MarketID:    req.MarketID,
		Side:        req.Side,
		Price:       p,
		Quantity:    q,
		RealizedPnL: pnl,
		ExecutedAt:  now,
	}
	t.trades = append(t.trades, trade)

	fill := &Fill{Trade: trade}
	if h.Qty != 0 {
		fill.Quantity = int(math.Abs(float64(h.Qty)))
		fill.AvgPrice = h.AvgPrice
		fill.Side = "long"
		fill.Exposure = h.AvgPrice * float64(fill.Quantity)
		if h.Qty < 0 {
			fill.Side = "short"
			fill.Exposure = (1 - h.AvgPrice) * float64(fill.Quantity)
		}
	}

	return fill, nil
}

// Mark records the latest market price, used to value open positions
func (t *Trader) Mark(marketID string, price float64) {
	if price <= 0 || price >= 1 {
		return
	}
	t.mu.Lock()
	t.lastPrices[marketID] = price
	t.mu.Unlock()
}

// Summary returns a snapshot of the portfolio, marking positions at the last known price
func (t *Trader) Summary() *Summary {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := &Summary{
		StartingBalance: t.startingBal,
		Cash:            t.cash,
		RealizedPnL:     t.realizedPnL,
		Trades:          len(t.trades),
	}

	for id, h := range t.positions {
		mark, ok := t.lastPrices[id]
		if !ok {
			mark = h.AvgPrice
		}
		ps := PositionSummary{MarketID: id, AvgPrice: h.AvgPrice, MarkPrice: mark}
		if h.Qty > 0 {
			ps.Side = "long"
			ps.Quantity = h.Qty
			ps.UnrealizedPnL = (mark - h.AvgPrice) * float64(h.Qty)
			ps.Exposure = h.AvgPrice * float64(h.Qty)
			s.PositionsValue += mark * float64(h.Qty)
		} else {
			ps.Side = "short"
			ps.Quantity = -h.Qty
			ps.UnrealizedPnL = (h.AvgPrice - mark) * float64(-h.Qty)
			ps.Exposure = (1 - h.AvgPrice) * float64(-h.Qty)
			s.PositionsValue += (1 - mark) * float64(-h.Qty)
		}
		s.UnrealizedPnL += ps.UnrealizedPnL
		s.Positions = append(s.Positions, ps)
	}
	sort.Slice(s.Positions, func(i, j int) bool { return s.Positions[i].MarketID < s.Positions[j].MarketID })

	s.Equity = s.Cash + s.PositionsValue
	if t.startingBal > 0 {
		s.TotalReturnPct = (s.Equity - t.startingBal) / t.startingBal * 100
	}

	return s
}

// Trades returns a copy of all simulated trades
func (t *Trader) Trades() []*types.Trade {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*types.Trade, len(t.trades))
	copy(out, t.trades)
	return out
}
