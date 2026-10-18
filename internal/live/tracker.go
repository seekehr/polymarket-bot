// Package live tracks real positions and orders while trading live.
//
// Polymarket is the source of truth. At startup the tracker takes the
// account's positions from the Data API as a baseline, then applies fills
// from the user websocket (and REST catch-up after a reconnect) on top.
// Positions are always recomputed from baseline + fills, so a fill that
// later fails settlement simply drops out.
package live

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/internal/api"
	"github.com/seeker/polymarket-bot/internal/websocket"
	"github.com/seeker/polymarket-bot/pkg/types"
)

// Holding is a long position in one outcome token.
type Holding struct {
	Shares   float64
	AvgPrice float64
}

// Update reports a fill that is new or whose settlement status changed.
type Update struct {
	Fill Fill
	New  bool
}

type openOrder struct {
	id, assetID, side    string
	price, size, matched float64
}

func (o *openOrder) remaining() float64 { return max(o.size-o.matched, 0) }

// Tracker holds the live view of positions and resting orders.
type Tracker struct {
	mu      sync.Mutex
	apiKey  string
	maker   string // lower-case funder address
	journal *Journal

	since    time.Time // fills matched before this are already in the baseline
	baseline map[string]Holding
	holdings map[string]Holding
	fills    []*Fill
	byKey    map[string]*Fill
	orders   map[string]*openOrder
}

// NewTracker creates a tracker for the account identified by apiKey, whose
// funds sit in the maker wallet. journal may be nil.
func NewTracker(apiKey, maker string, journal *Journal) *Tracker {
	return &Tracker{
		apiKey:   apiKey,
		maker:    strings.ToLower(maker),
		journal:  journal,
		baseline: make(map[string]Holding),
		holdings: make(map[string]Holding),
		byKey:    make(map[string]*Fill),
		orders:   make(map[string]*openOrder),
	}
}

// SetBaseline records the positions Polymarket reported at time at.
func (t *Tracker) SetBaseline(positions []api.Position, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.since = at
	t.baseline = make(map[string]Holding)
	for _, p := range positions {
		if p.CurrentSize > 1e-9 && p.TokenID != "" {
			t.baseline[p.TokenID] = Holding{Shares: p.CurrentSize, AvgPrice: p.AvgPrice}
		}
	}
	t.recompute()
}

// ApplyTrade folds a trade into the positions and returns our fills in it
// that are new or changed status. Trades we aren't part of yield nothing.
func (t *Tracker) ApplyTrade(tr *api.Trade) []Update {
	t.mu.Lock()
	defer t.mu.Unlock()

	var updates []Update
	for _, f := range t.ownFills(tr) {
		if old, ok := t.byKey[f.Key]; ok {
			if old.Status == f.Status || old.Status == "FAILED" || (old.Status == "CONFIRMED" && f.Status != "FAILED") {
				continue // no news, or an out-of-order older status
			}
			old.Status = f.Status
			updates = append(updates, Update{Fill: *old})
			continue
		}
		if !t.since.IsZero() && f.MatchedAt.Before(t.since.Truncate(time.Second)) {
			continue // happened before startup: already counted in the baseline
		}
		fl := f
		t.fills = append(t.fills, &fl)
		t.byKey[fl.Key] = &fl
		updates = append(updates, Update{Fill: fl, New: true})
	}
	if len(updates) == 0 {
		return nil
	}

	t.recompute()
	for i := range updates { // report realized P&L as recomputed
		updates[i].Fill.RealizedPnL = t.byKey[updates[i].Fill.Key].RealizedPnL
		if t.journal != nil {
			t.journal.Append(updates[i].Fill)
		}
	}
	return updates
}

// ownFills extracts this account's side of a trade. The trade's own fields
// describe the taker; each maker order filled is listed in maker_orders.
func (t *Tracker) ownFills(tr *api.Trade) []Fill {
	matched := parseUnix(tr.MatchTime)
	base := Fill{TradeID: tr.ID, Market: tr.Market, Status: strings.ToUpper(tr.Status), MatchedAt: matched}

	var out []Fill
	if tr.TraderSide == "TAKER" || (tr.TraderSide == "" && tr.Owner == t.apiKey) {
		f := base
		f.Key = tr.ID + ":" + tr.TakerOrderID
		f.OrderID, f.AssetID, f.Outcome = tr.TakerOrderID, tr.AssetID, tr.Outcome
		f.Side, f.Role, f.FeeRateBps = strings.ToLower(tr.Side), "TAKER", tr.FeeRateBps
		f.Price, f.Size = parseNum(tr.Price), parseNum(tr.Size)
		out = append(out, f)
	}
	for _, mo := range tr.MakerOrders {
		if mo.Owner != t.apiKey && (t.maker == "" || strings.ToLower(mo.MakerAddress) != t.maker) {
			continue
		}
		f := base
		f.Key = tr.ID + ":" + mo.OrderID
		f.OrderID, f.AssetID, f.Outcome = mo.OrderID, mo.AssetID, mo.Outcome
		if f.AssetID == "" {
			f.AssetID = tr.AssetID
		}
		f.Side = strings.ToLower(mo.Side)
		if f.Side == "" { // the maker took the other side of the taker
			f.Side = map[string]string{"buy": "sell", "sell": "buy"}[strings.ToLower(tr.Side)]
		}
		f.Role, f.FeeRateBps = "MAKER", mo.FeeRateBps
		f.Price, f.Size = parseNum(mo.Price), parseNum(mo.MatchedAmount)
		out = append(out, f)
	}
	return out
}

// recompute rebuilds holdings from the baseline plus every non-failed fill,
// in match order, and sets each fill's realized P&L. Caller holds t.mu.
func (t *Tracker) recompute() {
	h := make(map[string]Holding, len(t.baseline))
	for k, v := range t.baseline {
		h[k] = v
	}
	sort.SliceStable(t.fills, func(i, j int) bool { return t.fills[i].MatchedAt.Before(t.fills[j].MatchedAt) })

	for _, f := range t.fills {
		f.RealizedPnL = 0
		if f.Status == "FAILED" || f.Size <= 0 {
			continue
		}
		cur := h[f.AssetID]
		switch f.Side {
		case "buy":
			cur.AvgPrice = (cur.AvgPrice*cur.Shares + f.Price*f.Size) / (cur.Shares + f.Size)
			cur.Shares += f.Size
		case "sell":
			closed := min(f.Size, cur.Shares)
			f.RealizedPnL = (f.Price - cur.AvgPrice) * closed
			cur.Shares -= f.Size
		}
		if cur.Shares <= 1e-9 {
			cur = Holding{}
		}
		h[f.AssetID] = cur
	}
	for k, v := range h {
		if v.Shares == 0 {
			delete(h, k)
		}
	}
	t.holdings = h
}

// SetOpenOrders replaces the resting orders with what the CLOB reports.
func (t *Tracker) SetOpenOrders(orders []api.OpenOrder) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.orders = make(map[string]*openOrder, len(orders))
	for _, o := range orders {
		t.orders[o.ID] = &openOrder{
			id: o.ID, assetID: o.AssetID, side: strings.ToLower(o.Side),
			price: parseNum(o.Price), size: parseNum(o.OriginalSize), matched: parseNum(o.SizeMatched),
		}
	}
}

// OrderPlaced records an order the exchange accepted and left resting.
func (t *Tracker) OrderPlaced(id, assetID, side string, price float64, size int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.orders[id]; !ok && id != "" {
		t.orders[id] = &openOrder{id: id, assetID: assetID, side: side, price: price, size: float64(size)}
	}
}

// ApplyOrder updates a resting order from the user feed. It reports whether
// the order is now closed (fully filled or cancelled) and how much matched.
func (t *Tracker) ApplyOrder(ev *websocket.OrderEvent) (closed bool, matched float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	o, ok := t.orders[ev.ID]
	if !ok {
		o = &openOrder{id: ev.ID, assetID: ev.AssetID, side: strings.ToLower(ev.Side), price: parseNum(ev.Price)}
	}
	if s := parseNum(ev.OriginalSize); s > 0 {
		o.size = s
	}
	o.matched = max(o.matched, parseNum(ev.SizeMatched))

	status, typ := strings.ToUpper(ev.Status), strings.ToUpper(ev.Type)
	if typ == "CANCELLATION" || status == "CANCELED" || status == "UNMATCHED" || status == "MATCHED" ||
		(o.size > 0 && o.remaining() <= 1e-9) {
		delete(t.orders, ev.ID)
		return true, o.matched
	}
	t.orders[ev.ID] = o
	return false, o.matched
}

// Holding returns the current position in a token.
func (t *Tracker) Holding(assetID string) Holding {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holdings[assetID]
}

// Holdings returns a copy of all current positions.
func (t *Tracker) Holdings() map[string]Holding {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Holding, len(t.holdings))
	for k, v := range t.holdings {
		out[k] = v
	}
	return out
}

// SellableShares is what can still be sold: held shares not already
// committed to resting sell orders. Polymarket can't sell shares you don't own.
func (t *Tracker) SellableShares(assetID string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.holdings[assetID].Shares
	for _, o := range t.orders {
		if o.assetID == assetID && o.side == "sell" {
			n -= o.remaining()
		}
	}
	return max(n, 0)
}

// Exposure is the USD at risk in a token: position cost plus resting buys.
func (t *Tracker) Exposure(assetID string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.holdings[assetID]
	e := h.Shares * h.AvgPrice
	for _, o := range t.orders {
		if o.assetID == assetID && o.side == "buy" {
			e += o.remaining() * o.price
		}
	}
	return e
}

// OpenOrderCount returns how many orders are resting.
func (t *Tracker) OpenOrderCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.orders)
}

// SessionRealizedPnL sums realized P&L from fills seen since startup.
func (t *Tracker) SessionRealizedPnL() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum float64
	for _, f := range t.fills {
		sum += f.RealizedPnL
	}
	return sum
}

func parseNum(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// parseUnix reads match_time, which the API sends as unix seconds.
func parseUnix(s string) time.Time {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return time.Now()
	}
	if v > 1e12 { // milliseconds
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}

// String formats a fill for logs.
func (f Fill) String() string {
	return fmt.Sprintf("%s %.2f @ %.4f (%s, %s)", strings.ToUpper(f.Side), f.Size, f.Price, strings.ToLower(f.Role), f.Status)
}

// OpenOrders returns the resting orders in the order manager's format.
func (t *Tracker) OpenOrders() []*types.Order {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*types.Order, 0, len(t.orders))
	for _, o := range t.orders {
		out = append(out, &types.Order{
			ID: o.id, MarketID: o.assetID, Side: o.side, Price: o.price,
			Quantity: int(o.size), FilledQty: int(o.matched), Status: "open",
		})
	}
	return out
}

// HasOpenOrder reports whether an order on this side is already resting in the market.
func (t *Tracker) HasOpenOrder(assetID, side string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, o := range t.orders {
		if o.assetID == assetID && o.side == side {
			return true
		}
	}
	return false
}
