package websocket

import (
	"math"
	"testing"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

const (
	yesToken = "32338220190071351435772801779725302244575775216413325951443816017994629993401"
	noToken  = "25659310674993675562345759665114759892400026242514633218387667107987341231962"
)

func testMarketClient() *Client {
	cfg := &config.Config{}
	cfg.Markets.TargetMarketIDs = []string{yesToken}
	cfg.WebSocket.MessageBufferSize = 10
	return NewClient(cfg)
}

func next(t *testing.T, c *Client) *types.MarketData {
	t.Helper()
	select {
	case d := <-c.Messages():
		return d
	default:
		t.Fatal("no market data published")
		return nil
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestMarketFeedParsing(t *testing.T) {
	c := testMarketClient()

	// Initial book snapshot, as the live feed sends it: an array, no event_type,
	// bids ascending.
	c.processMessage([]byte(`[{"market":"0xa4","asset_id":"` + yesToken + `","timestamp":"1792364654001","hash":"x",
		"bids":[{"price":"0.001","size":"100"},{"price":"0.026","size":"5"}],
		"asks":[{"price":"0.999","size":"1"},{"price":"0.027","size":"9"}]}]`))
	d := next(t, c)
	if d.MarketID != yesToken || d.Timestamp.UnixMilli() != 1792364654001 {
		t.Errorf("book update = %+v", d)
	}
	approx(t, "bid", d.BidPrice, 0.026)
	approx(t, "ask", d.AskPrice, 0.027)
	approx(t, "mid", d.Price, 0.0265)

	// A price change for both outcomes: only the subscribed token is published.
	c.processMessage([]byte(`{"market":"0xa4","price_changes":[
		{"asset_id":"` + yesToken + `","price":"0.025","size":"1","side":"BUY","best_bid":"0.030","best_ask":"0.034"},
		{"asset_id":"` + noToken + `","price":"0.975","size":"1","side":"SELL","best_bid":"0.966","best_ask":"0.97"}],
		"timestamp":"1792364655000","event_type":"price_change"}`))
	d = next(t, c)
	approx(t, "mid after change", d.Price, 0.032)
	if len(c.Messages()) != 0 {
		t.Error("unsubscribed token was published")
	}

	// Trades update the last price; with a two-sided book the midpoint still wins.
	c.processMessage([]byte(`{"event_type":"last_trade_price","asset_id":"` + yesToken + `","price":"0.031","size":"10","side":"BUY","timestamp":"1792364656000"}`))
	approx(t, "mid with trade", next(t, c).Price, 0.032)

	// One-sided book: fall back to the last trade.
	c.processMessage([]byte(`{"event_type":"best_bid_ask","asset_id":"` + yesToken + `","best_bid":"0.03","best_ask":"0"}`))
	approx(t, "one-sided", next(t, c).Price, 0.031)

	// Heartbeats, tick size changes and junk publish nothing.
	for _, msg := range []string{"PONG", "[]", `{"event_type":"tick_size_change","asset_id":"` + yesToken + `"}`} {
		c.processMessage([]byte(msg))
	}
	if len(c.Messages()) != 0 {
		t.Error("non-price message was published")
	}
	c.processMessage([]byte("not json"))
	if len(c.Errors()) != 1 {
		t.Error("bad JSON not reported")
	}
}
