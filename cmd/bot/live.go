package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/internal/api"
	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/internal/live"
	"github.com/seeker/polymarket-bot/internal/metrics"
	"github.com/seeker/polymarket-bot/internal/order"
	"github.com/seeker/polymarket-bot/internal/strategy"
	"github.com/seeker/polymarket-bot/internal/websocket"
	"github.com/seeker/polymarket-bot/pkg/types"
)

// liveTrading connects the bot to a real Polymarket account: signed orders
// out, fills and order updates in, positions reconciled with Polymarket.
type liveTrading struct {
	client   *api.Client
	tracker  *live.Tracker
	journal  *live.Journal
	feed     *websocket.UserClient
	strategy *strategy.Engine
	metrics  *metrics.Collector
	orders   *order.Manager // set by attach
	targets  []string
	syncMu   sync.Mutex
	synced   map[string]bool // target markets the strategy currently holds
	started  time.Time
}

// startLive checks credentials, loads the account's real positions and open
// orders, and connects the user feed. Any failure is fatal: trading without
// knowing what we already hold isn't safe.
func startLive(ctx context.Context, cfg *config.Config, client *api.Client, strategyEngine *strategy.Engine, mc *metrics.Collector) *liveTrading {
	if err := cfg.ValidateLive(); err != nil {
		logConfig.Fatalf("%v", err)
	}
	if err := client.EnableTrading(); err != nil {
		logConfig.Fatalf("%v", err)
	}
	logLive.Infof("Signing as %s, funds in %s (signature type %d)", client.SignerAddress(), client.MakerAddress(), cfg.API.SignatureType)

	version, err := client.Version()
	if err != nil {
		logAPI.Fatalf("%v", err)
	}
	for _, id := range cfg.Markets.TargetMarketIDs {
		tick, err := client.TickSize(id)
		if err != nil {
			logAPI.Fatalf("Market %s isn't tradable: %v (target_market_ids must be CLOB token IDs)", short(id), err)
		}
		negRisk, err := client.NegRisk(id)
		if err != nil {
			logAPI.Fatalf("%v", err)
		}
		logAPI.Infof("Market %s: tick %s, neg-risk %v, exchange v%d", short(id), tick, negRisk, version)
	}

	journal, err := live.OpenJournal(cfg.LiveTrading.JournalFile)
	if err != nil {
		logState.Fatalf("%v", err)
	}

	l := &liveTrading{
		client:   client,
		tracker:  live.NewTracker(client.APIKey(), client.MakerAddress(), journal),
		journal:  journal,
		feed:     websocket.NewUserClient(cfg.API.UserWebSocketURL, cfg.API.APIKey, cfg.API.APISecret, cfg.API.Passphrase),
		strategy: strategyEngine,
		metrics:  mc,
		targets:  cfg.Markets.TargetMarketIDs,
		synced:   make(map[string]bool),
		started:  time.Now(),
	}

	// Connect the feed before taking the snapshot so no fill falls in between.
	if err := l.feed.Connect(ctx); err != nil {
		logWS.Fatalf("%v", err)
	}
	positions, err := client.GetPositions()
	if err != nil {
		logAPI.Fatalf("Can't load positions: %v", err)
	}
	l.tracker.SetBaseline(positions, l.started)
	open, err := client.GetOpenOrders()
	if err != nil {
		logAPI.Fatalf("Can't load open orders: %v", err)
	}
	l.tracker.SetOpenOrders(open)

	logPnL.Infof("Polymarket reports %d open positions and %d resting orders", len(positions), len(open))
	for _, p := range positions {
		if p.CurrentSize > 0 {
			logPnL.Infof("  %s %.2f shares @ %.4f (mark %.4f) %s", short(p.TokenID), p.CurrentSize, p.AvgPrice, p.CurrentPrice, p.Title)
		}
	}
	return l
}

// attach hooks the order manager up to the live state.
func (l *liveTrading) attach(ctx context.Context, m *order.Manager) {
	l.orders = m
	m.SyncOutstanding(l.tracker.OpenOrders())

	// Carry today's realized losses over a restart so the daily loss limit holds.
	fills, err := live.ReadJournal(l.journal.Path())
	if err != nil {
		logState.Warnf("%v (daily loss starts from zero)", err)
	}
	y, mo, d := time.Now().Date()
	var loss float64
	for _, f := range fills {
		if fy, fm, fd := f.MatchedAt.Local().Date(); fy == y && fm == mo && fd == d && f.Status != "FAILED" && f.RealizedPnL < 0 {
			m.RecordTrade(&types.Trade{RealizedPnL: f.RealizedPnL})
			loss -= f.RealizedPnL
		}
	}
	if loss > 0 {
		logState.Infof("Today's realized loss so far: $%.2f (from %s)", loss, l.journal.Path())
	}

	l.sync()
	go l.run(ctx)
}

// submitFunc places real orders. Sells are capped at shares actually held,
// since Polymarket can't sell what you don't own (there is no shorting).
func (l *liveTrading) submitFunc() order.SubmitFunc {
	return func(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
		responses := make([]*types.OrderResponse, len(requests))
		for i, req := range requests {
			// The strategy repeats its signal on every tick until the position
			// changes; a resting order already covers it.
			if l.tracker.HasOpenOrder(req.MarketID, req.Side) {
				responses[i] = &types.OrderResponse{Status: order.StatusSkipped, Error: "an order on this side is already resting"}
				logOrder.Debugf("Skipped %s in %s: an order on that side is already resting", req.Side, short(req.MarketID))
				continue
			}
			if req.Side == "sell" {
				avail := int(math.Floor(l.tracker.SellableShares(req.MarketID) + 1e-9))
				if avail < 1 {
					responses[i] = &types.OrderResponse{Status: order.StatusSkipped, Error: "no shares to sell (live trading can't short)"}
					logOrder.Debugf("Skipped SELL %d in %s: no shares held (live trading can't short)", req.Quantity, short(req.MarketID))
					continue
				}
				if req.Quantity > avail {
					logOrder.Infof("Capping SELL in %s from %d to the %d shares held", short(req.MarketID), req.Quantity, avail)
					req.Quantity = avail
				}
			}

			start := time.Now()
			resp, err := l.client.PlaceOrder(req)
			if err != nil {
				l.metrics.RecordOrder(false)
				l.metrics.RecordError()
				logOrder.Errorf("%s %d @ %.4f in %s failed: %v", req.Side, req.Quantity, req.Price, short(req.MarketID), err)
				responses[i] = &types.OrderResponse{Error: err.Error()}
				continue
			}
			l.metrics.RecordLatency(time.Since(start))
			l.metrics.RecordOrder(resp.Success)
			if !resp.Success {
				logOrder.Errorf("%s %d @ %.4f in %s rejected: %s", req.Side, req.Quantity, req.Price, short(req.MarketID), resp.Error)
			} else {
				logOrder.Infof("%s %d @ %.4f in %s accepted (%s, id %s)", req.Side, req.Quantity, req.Price, short(req.MarketID), resp.Status, short(resp.OrderID))
				if resp.Status == "live" || resp.Status == "delayed" {
					l.tracker.OrderPlaced(resp.OrderID, req.MarketID, req.Side, req.Price, req.Quantity)
				}
			}
			responses[i] = resp
		}
		l.sync()
		return responses, nil
	}
}

// run applies user-feed events until ctx ends.
func (l *liveTrading) run(ctx context.Context) {
	lastResync := l.started
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-l.feed.Errors():
			logWS.Warnf("%v", err)
		case ev := <-l.feed.Events():
			switch {
			case ev.Trade != nil:
				l.applyTrade(ev.Trade)
			case ev.Order != nil:
				if closed, matched := l.tracker.ApplyOrder(ev.Order); closed {
					status := "cancelled"
					if ev.Order.Status == "MATCHED" {
						status = "filled"
					}
					l.orders.UpdateOrderStatus(ev.Order.ID, status, int(matched))
				}
			case ev.Reconnected:
				logWS.Info("User feed reconnected; catching up on missed fills and orders")
				l.resync(lastResync.Add(-time.Minute))
				lastResync = time.Now()
			}
			l.sync()
		}
	}
}

func (l *liveTrading) applyTrade(t *api.Trade) {
	for _, u := range l.tracker.ApplyTrade(t) {
		f := u.Fill
		switch {
		case f.Status == "FAILED":
			logFill.Errorf("%s in %s failed to settle; position reverted", f, short(f.AssetID))
		case u.New:
			logFill.Infof("%s in %s (realized P&L $%.2f)", f, short(f.AssetID), f.RealizedPnL)
			l.metrics.RecordTrade(f.RealizedPnL)
			l.orders.RecordTrade(&types.Trade{ID: f.TradeID, OrderID: f.OrderID, MarketID: f.AssetID, RealizedPnL: f.RealizedPnL})
		default:
			logFill.Debugf("%s in %s", f, short(f.AssetID))
		}
	}
}

// resync replays trades since a point in time and reloads open orders, for
// anything the feed missed while disconnected.
func (l *liveTrading) resync(since time.Time) {
	trades, err := l.client.GetTrades(since)
	if err != nil {
		logAPI.Errorf("Catch-up on trades failed: %v", err)
	}
	for i := range trades {
		l.applyTrade(&trades[i])
	}
	open, err := l.client.GetOpenOrders()
	if err != nil {
		logAPI.Errorf("Reloading open orders failed: %v", err)
		return
	}
	l.tracker.SetOpenOrders(open)
	l.orders.SyncOutstanding(l.tracker.OpenOrders())
}

// sync pushes the tracked positions to the strategy and risk manager.
func (l *liveTrading) sync() {
	l.syncMu.Lock() // called from both the order path and the feed goroutine
	defer l.syncMu.Unlock()
	for _, id := range l.targets {
		h := l.tracker.Holding(id)
		shares := int(math.Floor(h.Shares + 1e-9))
		if shares > 0 {
			l.strategy.UpdatePosition(id, "long", shares, h.AvgPrice)
			l.synced[id] = true
		} else if l.synced[id] {
			l.strategy.UpdatePosition(id, "", 0, 0)
			delete(l.synced, id)
		}
		if l.orders != nil {
			l.orders.SetMarketExposure(id, l.tracker.Exposure(id))
		}
	}
}

// logSummary prints live positions and session P&L at shutdown.
func (l *liveTrading) logSummary() {
	logPnL.Infof("Session realized P&L: $%.2f (excluding fees)", l.tracker.SessionRealizedPnL())
	holdings := l.tracker.Holdings()
	ids := make([]string, 0, len(holdings))
	for id := range holdings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		h := holdings[id]
		logPnL.Infof("  %s %.2f shares @ %.4f", short(id), h.Shares, h.AvgPrice)
	}
	if n := l.tracker.OpenOrderCount(); n > 0 {
		logLive.Warnf("%d orders are still resting on Polymarket and can fill while the bot is stopped", n)
	}
}

func (l *liveTrading) close() {
	if err := l.journal.Close(); err != nil {
		logState.Errorf("%v", err)
	}
}

// short abbreviates long token and order IDs for logs.
func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return fmt.Sprintf("%s…%s", id[:6], id[len(id)-4:])
}
