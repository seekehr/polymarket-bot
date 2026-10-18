package websocket

import "testing"

func TestParseUserMessage(t *testing.T) {
	trade := `{"event_type":"trade","type":"TRADE","id":"t1","taker_order_id":"o1","asset_id":"123","side":"BUY",
		"size":"10","price":"0.52","status":"MATCHED","match_time":"1760000000","owner":"k","trader_side":"TAKER",
		"maker_orders":[{"order_id":"m1","owner":"x","matched_amount":"10","price":"0.52","asset_id":"123","side":"SELL"}]}`
	order := `{"event_type":"order","type":"UPDATE","id":"o2","asset_id":"123","side":"SELL","original_size":"5","size_matched":"2","price":"0.6","status":"LIVE"}`

	evs := parseUserMessage([]byte(trade))
	if len(evs) != 1 || evs[0].Trade == nil || evs[0].Trade.MakerOrders[0].OrderID != "m1" || evs[0].Trade.TraderSide != "TAKER" {
		t.Fatalf("trade = %+v", evs)
	}
	evs = parseUserMessage([]byte("[" + order + "," + trade + `,{"event_type":"other"}]`))
	if len(evs) != 2 || evs[0].Order == nil || evs[0].Order.SizeMatched != "2" || evs[1].Trade == nil {
		t.Fatalf("batch = %+v", evs)
	}
	for _, junk := range []string{"PONG", "", "not json", `{"event_type":"trade"}`} {
		if evs := parseUserMessage([]byte(junk)); len(evs) != 0 {
			t.Errorf("%q produced %+v", junk, evs)
		}
	}
}
