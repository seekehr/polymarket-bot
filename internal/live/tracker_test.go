package live

import (
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/seeker/polymarket-bot/internal/api"
	"github.com/seeker/polymarket-bot/internal/websocket"
)

const (
	myKey   = "my-api-key"
	myMaker = "0xAbC0000000000000000000000000000000000001"
	tokenA  = "111"
)

var t0 = time.Unix(1_760_000_000, 0)

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// takerTrade is a trade where we were the taker.
func takerTrade(id, side string, price, size float64, at time.Time, status string) *api.Trade {
	return &api.Trade{
		ID: id, TakerOrderID: "ord-" + id, AssetID: tokenA, Side: side, Owner: myKey, TraderSide: "TAKER",
		Price: strconv.FormatFloat(price, 'f', -1, 64), Size: strconv.FormatFloat(size, 'f', -1, 64),
		Status: status, MatchTime: strconv.FormatInt(at.Unix(), 10),
	}
}

func TestTakerFillsBuildPosition(t *testing.T) {
	tr := NewTracker(myKey, myMaker, nil)
	tr.SetBaseline(nil, t0)

	if u := tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "MATCHED")); len(u) != 1 || !u[0].New {
		t.Fatalf("expected one new fill, got %+v", u)
	}
	tr.ApplyTrade(takerTrade("t2", "BUY", 0.50, 100, t0.Add(2*time.Second), "MATCHED"))
	h := tr.Holding(tokenA)
	near(t, "shares", h.Shares, 200)
	near(t, "avg", h.AvgPrice, 0.45)

	u := tr.ApplyTrade(takerTrade("t3", "SELL", 0.60, 50, t0.Add(3*time.Second), "MATCHED"))
	near(t, "realized", u[0].Fill.RealizedPnL, (0.60-0.45)*50)
	near(t, "shares after sell", tr.Holding(tokenA).Shares, 150)
	near(t, "session pnl", tr.SessionRealizedPnL(), 7.5)
}

func TestStatusUpdatesAndFailedFills(t *testing.T) {
	tr := NewTracker(myKey, myMaker, nil)
	tr.SetBaseline(nil, t0)
	tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "MATCHED"))

	// Same status again: nothing new.
	if u := tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "MATCHED")); u != nil {
		t.Errorf("duplicate trade produced updates: %+v", u)
	}
	// A later status is reported once, not as a new fill.
	u := tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "MINED"))
	if len(u) != 1 || u[0].New || u[0].Fill.Status != "MINED" {
		t.Errorf("status change = %+v", u)
	}
	// Failed settlement removes the shares again.
	tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "FAILED"))
	if h := tr.Holding(tokenA); h.Shares != 0 {
		t.Errorf("failed fill still counted: %+v", h)
	}
	// An out-of-order older status can't resurrect it.
	if u := tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 100, t0.Add(time.Second), "MATCHED")); u != nil {
		t.Errorf("stale status applied: %+v", u)
	}
}

func TestMakerFillsAndBaseline(t *testing.T) {
	tr := NewTracker(myKey, myMaker, nil)
	tr.SetBaseline([]api.Position{{TokenID: tokenA, CurrentSize: 10, AvgPrice: 0.30}, {TokenID: "dust", CurrentSize: 0}}, t0)
	near(t, "baseline shares", tr.Holding(tokenA).Shares, 10)
	if len(tr.Holdings()) != 1 {
		t.Errorf("zero-size baseline row kept: %+v", tr.Holdings())
	}

	// Someone else took our resting sell (side omitted: inferred from the taker's BUY),
	// and another maker's order in the same trade is ignored.
	trade := &api.Trade{
		ID: "t9", TakerOrderID: "their-order", AssetID: tokenA, Side: "BUY", Owner: "someone", TraderSide: "MAKER",
		Price: "0.55", Size: "8", Status: "MATCHED", MatchTime: strconv.FormatInt(t0.Add(time.Minute).Unix(), 10),
		MakerOrders: []api.MakerOrder{
			{OrderID: "mine", Owner: myKey, MatchedAmount: "4", Price: "0.55"},
			{OrderID: "theirs", Owner: "other", MatchedAmount: "4", Price: "0.55"},
		},
	}
	u := tr.ApplyTrade(trade)
	if len(u) != 1 || u[0].Fill.Side != "sell" || u[0].Fill.Role != "MAKER" || u[0].Fill.OrderID != "mine" {
		t.Fatalf("maker fill = %+v", u)
	}
	near(t, "realized vs baseline cost", u[0].Fill.RealizedPnL, (0.55-0.30)*4)
	near(t, "shares", tr.Holding(tokenA).Shares, 6)

	// Trades from before the baseline snapshot are already in it.
	if u := tr.ApplyTrade(takerTrade("old", "BUY", 0.2, 5, t0.Add(-time.Hour), "CONFIRMED")); u != nil {
		t.Errorf("pre-baseline trade applied: %+v", u)
	}
}

func TestOrdersSellableAndExposure(t *testing.T) {
	tr := NewTracker(myKey, myMaker, nil)
	tr.SetBaseline([]api.Position{{TokenID: tokenA, CurrentSize: 100, AvgPrice: 0.40}}, t0)
	tr.SetOpenOrders([]api.OpenOrder{
		{ID: "s1", AssetID: tokenA, Side: "SELL", Price: "0.70", OriginalSize: "30", SizeMatched: "10"},
		{ID: "b1", AssetID: tokenA, Side: "BUY", Price: "0.30", OriginalSize: "50"},
	})
	near(t, "sellable", tr.SellableShares(tokenA), 100-20)
	near(t, "exposure", tr.Exposure(tokenA), 100*0.40+50*0.30)

	tr.OrderPlaced("b2", tokenA, "buy", 0.25, 40)
	near(t, "exposure with new order", tr.Exposure(tokenA), 40+15+10)

	if closed, _ := tr.ApplyOrder(&websocket.OrderEvent{ID: "b1", Type: "UPDATE", SizeMatched: "20", OriginalSize: "50", Status: "LIVE"}); closed {
		t.Error("partially filled order closed")
	}
	if closed, m := tr.ApplyOrder(&websocket.OrderEvent{ID: "b1", Type: "CANCELLATION"}); !closed || m != 20 {
		t.Errorf("cancel = %v, matched %v", closed, m)
	}
	if closed, _ := tr.ApplyOrder(&websocket.OrderEvent{ID: "s1", Type: "UPDATE", SizeMatched: "30"}); !closed {
		t.Error("fully matched order still open")
	}
	if n := tr.OpenOrderCount(); n != 1 {
		t.Errorf("open orders = %d, want 1", n)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "live.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(myKey, myMaker, j)
	tr.SetBaseline(nil, t0)
	tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 10, t0.Add(time.Second), "MATCHED"))
	tr.ApplyTrade(takerTrade("t1", "BUY", 0.40, 10, t0.Add(time.Second), "CONFIRMED"))
	tr.ApplyTrade(takerTrade("t2", "SELL", 0.50, 10, t0.Add(2*time.Second), "MATCHED"))
	j.Close()

	fills, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 2 || fills[0].Status != "CONFIRMED" || fills[1].TradeID != "t2" {
		t.Fatalf("journal = %+v", fills)
	}
	near(t, "journaled pnl", fills[1].RealizedPnL, 1.0)

	if f, err := ReadJournal(filepath.Join(t.TempDir(), "missing.jsonl")); f != nil || err != nil {
		t.Errorf("missing journal = %v, %v", f, err)
	}
}
