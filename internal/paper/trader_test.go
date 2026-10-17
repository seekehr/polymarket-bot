package paper

import (
	"math"
	"testing"

	"github.com/seeker/polymarket-bot/pkg/types"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.6f, want %.6f", name, got, want)
	}
}

func order(side string, price float64, qty int) *types.OrderRequest {
	return &types.OrderRequest{MarketID: "m1", Side: side, Price: price, Quantity: qty}
}

func TestLongRoundTrip(t *testing.T) {
	tr := NewTrader(100)

	fill, err := tr.Execute(order("buy", 0.40, 100))
	if err != nil {
		t.Fatal(err)
	}
	if fill.Side != "long" || fill.Quantity != 100 {
		t.Fatalf("unexpected position after buy: %+v", fill)
	}
	approx(t, "cash after buy", tr.Summary().Cash, 60)

	fill, err = tr.Execute(order("sell", 0.60, 100))
	if err != nil {
		t.Fatal(err)
	}
	if fill.Side != "" || fill.Quantity != 0 {
		t.Fatalf("expected flat after sell, got %+v", fill)
	}
	approx(t, "trade pnl", fill.Trade.RealizedPnL, 20)

	s := tr.Summary()
	approx(t, "cash", s.Cash, 120)
	approx(t, "equity", s.Equity, 120)
	approx(t, "realized", s.RealizedPnL, 20)
	approx(t, "return pct", s.TotalReturnPct, 20)
}

func TestShortRoundTrip(t *testing.T) {
	tr := NewTrader(100)

	// Short 100 @ 0.70 locks (1 - 0.70) * 100 = $30
	fill, err := tr.Execute(order("sell", 0.70, 100))
	if err != nil {
		t.Fatal(err)
	}
	if fill.Side != "short" || fill.Quantity != 100 {
		t.Fatalf("unexpected position after short: %+v", fill)
	}
	approx(t, "cash after short", tr.Summary().Cash, 70)

	fill, err = tr.Execute(order("buy", 0.50, 100))
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "trade pnl", fill.Trade.RealizedPnL, 20)
	approx(t, "cash", tr.Summary().Cash, 120)
}

func TestInsufficientBalance(t *testing.T) {
	tr := NewTrader(10)
	if _, err := tr.Execute(order("buy", 0.50, 100)); err == nil {
		t.Fatal("expected insufficient balance error")
	}
	s := tr.Summary()
	approx(t, "cash unchanged", s.Cash, 10)
	if s.Trades != 0 {
		t.Fatalf("trades = %d, want 0", s.Trades)
	}
}

func TestAveragingAndMarkToMarket(t *testing.T) {
	tr := NewTrader(1000)
	tr.Execute(order("buy", 0.40, 100))
	fill, _ := tr.Execute(order("buy", 0.50, 100))
	approx(t, "avg price", fill.AvgPrice, 0.45)

	tr.Mark("m1", 0.55)
	s := tr.Summary()
	approx(t, "unrealized", s.UnrealizedPnL, 20)
	approx(t, "equity", s.Equity, 1020)
}

func TestFlipPosition(t *testing.T) {
	tr := NewTrader(1000)
	tr.Execute(order("buy", 0.40, 100))
	fill, err := tr.Execute(order("sell", 0.60, 150))
	if err != nil {
		t.Fatal(err)
	}
	if fill.Side != "short" || fill.Quantity != 50 {
		t.Fatalf("expected short 50, got %+v", fill)
	}
	approx(t, "avg price", fill.AvgPrice, 0.60)
	approx(t, "pnl", fill.Trade.RealizedPnL, 20)
}

func TestRejectsInvalidPrice(t *testing.T) {
	tr := NewTrader(100)
	for _, p := range []float64{0, 1, 1.2, -0.1} {
		if _, err := tr.Execute(order("buy", p, 1)); err == nil {
			t.Errorf("price %.2f: expected error", p)
		}
	}
}
