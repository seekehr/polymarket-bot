package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/internal/live"
	"github.com/seeker/polymarket-bot/internal/metrics"
	"github.com/seeker/polymarket-bot/internal/order"
	"github.com/seeker/polymarket-bot/internal/paper"
	"github.com/seeker/polymarket-bot/internal/telegram"
)

// telegramBridge connects the Telegram bot to the trading bot: it answers
// commands from the bot's state and turns fills and alerts into messages.
// A nil *telegramBridge does nothing, so call sites don't check whether
// Telegram is enabled.
type telegramBridge struct {
	bot     *telegram.Bot // nil until start succeeds
	cfg     *config.Config
	labels  map[string]string // token ID -> "Question / Outcome"
	metrics *metrics.Collector
	paused  *atomic.Bool

	// set by attach once the components exist
	paper  *paper.Trader
	live   *liveTrading
	orders *order.Manager
}

// newTelegramBridge returns nil when Telegram is disabled.
func newTelegramBridge(cfg *config.Config, labels map[string]string, mc *metrics.Collector, paused *atomic.Bool) *telegramBridge {
	if !cfg.Telegram.Enabled {
		return nil
	}
	return &telegramBridge{cfg: cfg, labels: labels, metrics: mc, paused: paused}
}

func (t *telegramBridge) attach(p *paper.Trader, l *liveTrading, m *order.Manager) {
	if t == nil {
		return
	}
	t.paper, t.live, t.orders = p, l, m
}

// start connects to Telegram. A failure is logged, not fatal: trading
// doesn't depend on Telegram.
func (t *telegramBridge) start(ctx context.Context) {
	if t == nil {
		return
	}
	tc := t.cfg.Telegram
	bot, err := telegram.New(telegram.Config{
		Token:         tc.BotToken,
		ChatIDs:       tc.ChatIDs,
		AlertCooldown: time.Duration(tc.AlertCooldownSec) * time.Second,
		Proxy:         tc.Proxy,
	}, t, logTG)
	if err == nil {
		err = bot.Start(ctx)
	}
	if err != nil {
		logTG.Errorf("%v; continuing without Telegram", err)
		return
	}
	t.bot = bot
	t.bot.Notify("", fmt.Sprintf("🤖 Bot started: %s mode, %s strategy, %d market(s)\n%s",
		t.mode(), t.cfg.Strategy.Mode, len(t.cfg.Markets.TargetMarketIDs), t.marketList()))
}

// stop sends a final summary and waits briefly for queued messages.
func (t *telegramBridge) stop() {
	if t == nil || t.bot == nil {
		return
	}
	t.bot.Notify("", "🛑 Bot stopped\n\n"+t.PnL())
	t.bot.Close(5 * time.Second)
}

// fill reports a fill, if notify_fills is on.
func (t *telegramBridge) fill(text string) {
	if t == nil || t.bot == nil || !t.cfg.Telegram.NotifyFills {
		return
	}
	t.bot.Notify("", text)
}

// alert reports a problem, if notify_alerts is on. Alerts with the same key
// are sent at most once per alert_cooldown_sec.
func (t *telegramBridge) alert(key, text string) {
	if t == nil || t.bot == nil || !t.cfg.Telegram.NotifyAlerts {
		return
	}
	t.bot.Notify(key, "⚠️ "+text)
}

func (t *telegramBridge) paperFill(side string, qty int, price float64, marketID string, realized float64) {
	if t == nil {
		return
	}
	t.fill(fmt.Sprintf("%s Paper %s %d @ %.4f\n%s\nRealized P&L: %s | Equity: %s",
		sideIcon(side), strings.ToUpper(side), qty, price, t.name(marketID), usd(realized), usd(t.paper.Summary().Equity)))
}

func (t *telegramBridge) liveFill(f live.Fill) {
	if t == nil {
		return
	}
	t.fill(fmt.Sprintf("%s %s\n%s\nRealized P&L: %s", sideIcon(f.Side), f, t.name(f.AssetID), usd(f.RealizedPnL)))
}

func (t *telegramBridge) liveFillFailed(f live.Fill) {
	if t == nil {
		return
	}
	t.alert("", fmt.Sprintf("Fill failed to settle; position reverted\n%s\n%s", f, t.name(f.AssetID)))
}

// Status implements telegram.Controller.
func (t *telegramBridge) Status() string {
	s := t.metrics.GetStats()
	st := t.cfg.Strategy
	trading := "active"
	if t.paused.Load() {
		trading = "PAUSED (/resume to continue)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Mode: %s\nTrading: %s\nUptime: %s\n", t.mode(), trading, s.Uptime.Round(time.Second))
	fmt.Fprintf(&sb, "Strategy: %s (buy < %.4f, sell > %.4f, qty %d)\n", st.Mode, st.BuyBelow, st.SellAbove, st.Quantity)
	fmt.Fprintf(&sb, "Markets:\n%s\n", t.marketList())
	fmt.Fprintf(&sb, "Feed: %d msgs, avg latency %dms, error rate %.2f%%\n", s.MessagesReceived, s.AvgLatency.Milliseconds(), s.ErrorRate)
	fmt.Fprintf(&sb, "Orders: %d sent, %d ok, %d failed\n", s.OrdersSubmitted, s.OrdersSuccessful, s.OrdersFailed)
	fmt.Fprintf(&sb, "Daily loss: %s of %s limit", usd(t.orders.GetDailyLoss()), usd(t.cfg.Risk.MaxDailyLoss))
	return sb.String()
}

// PnL implements telegram.Controller.
func (t *telegramBridge) PnL() string {
	var sb strings.Builder
	if t.paper != nil {
		s := t.paper.Summary()
		fmt.Fprintf(&sb, "Paper equity: %s (%+.2f%%)\nCash: %s\nRealized: %s | Unrealized: %s\nTrades: %d",
			usd(s.Equity), s.TotalReturnPct, usd(s.Cash), usd(s.RealizedPnL), usd(s.UnrealizedPnL), s.Trades)
		if len(s.Positions) == 0 {
			sb.WriteString("\n\nNo open positions.")
		}
		for _, p := range s.Positions {
			fmt.Fprintf(&sb, "\n\n%s\n%s %d @ %.4f, mark %.4f, P&L %s",
				t.name(p.MarketID), p.Side, p.Quantity, p.AvgPrice, p.MarkPrice, usd(p.UnrealizedPnL))
		}
		return sb.String()
	}
	if t.live == nil {
		return "Not trading yet."
	}
	fmt.Fprintf(&sb, "Live session realized P&L: %s (excluding fees)", usd(t.live.tracker.SessionRealizedPnL()))
	holdings := t.live.tracker.Holdings()
	ids := make([]string, 0, len(holdings))
	for id, h := range holdings {
		if h.Shares > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		sb.WriteString("\n\nNo open positions.")
	}
	for _, id := range ids {
		h := holdings[id]
		fmt.Fprintf(&sb, "\n\n%s\n%.2f shares @ %.4f", t.name(id), h.Shares, h.AvgPrice)
	}
	return sb.String()
}

// Orders implements telegram.Controller.
func (t *telegramBridge) Orders() string {
	if t.paper != nil {
		return "Paper orders fill immediately, so none are ever working."
	}
	orders := t.orders.GetOutstandingOrders()
	if len(orders) == 0 {
		return "No working orders."
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt.Before(orders[j].CreatedAt) })
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d working order(s):", len(orders))
	for _, o := range orders {
		fmt.Fprintf(&sb, "\n\n%s %d @ %.4f (filled %d)\n%s\nid %s",
			strings.ToUpper(o.Side), o.Quantity, o.Price, o.FilledQty, t.name(o.MarketID), short(o.ID))
	}
	return sb.String()
}

// Pause implements telegram.Controller.
func (t *telegramBridge) Pause() string {
	if t.paused.Swap(true) {
		return "Already paused."
	}
	logSystem.Warnf("Trading paused from Telegram")
	msg := "⏸ Paused. No new orders will be placed until /resume."
	if t.live != nil {
		msg += "\nOrders already resting on Polymarket stay on the book and can still fill."
	}
	return msg
}

// Resume implements telegram.Controller.
func (t *telegramBridge) Resume() string {
	if !t.paused.Swap(false) {
		return "Not paused."
	}
	logSystem.Info("Trading resumed from Telegram")
	return "▶️ Resumed. The strategy will act on the next price update."
}

func (t *telegramBridge) mode() string {
	if t.cfg.PaperTrading.Enabled {
		return "PAPER"
	}
	return "LIVE"
}

func (t *telegramBridge) name(id string) string {
	if l, ok := t.labels[id]; ok {
		return l
	}
	return short(id)
}

func (t *telegramBridge) marketList() string {
	lines := make([]string, len(t.cfg.Markets.TargetMarketIDs))
	for i, id := range t.cfg.Markets.TargetMarketIDs {
		lines[i] = "• " + t.name(id)
	}
	return strings.Join(lines, "\n")
}

func sideIcon(side string) string {
	if strings.EqualFold(side, "buy") {
		return "🟢"
	}
	return "🔴"
}

// usd formats dollars without printing "-0.00".
func usd(v float64) string {
	if math.Abs(v) < 0.005 {
		v = 0
	}
	return fmt.Sprintf("$%.2f", v)
}

// alertKey groups alerts of one kind ("Avg latency 143ms exceeds…" and
// "Avg latency 151ms exceeds…") for the cooldown.
func alertKey(prefix, text string) string {
	f := strings.Fields(text)
	if len(f) > 2 {
		f = f[:2]
	}
	return prefix + ":" + strings.Join(f, " ")
}
