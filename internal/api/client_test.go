package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

const testSecret = "c3VwZXItc2VjcmV0LWtleS1ieXRlcy0wMTIzNDU2Nzg5"

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &config.Config{API: config.APIConfig{
		RESTEndpoint: srv.URL, DataAPIURL: srv.URL, TimeoutMS: 2000,
		APIKey: "key-123", APISecret: testSecret, Passphrase: "pass", PrivateKey: testKey,
	}}
	c := NewClient(cfg)
	if err := c.EnableTrading(); err != nil {
		t.Fatal(err)
	}
	return c
}

// checkAuth verifies the L2 headers sign exactly what was sent.
func checkAuth(t *testing.T, r *http.Request, body []byte) {
	t.Helper()
	ts, _ := strconv.ParseInt(r.Header.Get("POLY_TIMESTAMP"), 10, 64)
	want, _ := l2Signature(testSecret, ts, r.Method, r.URL.Path, body)
	if r.Header.Get("POLY_SIGNATURE") != want || r.Header.Get("POLY_API_KEY") != "key-123" ||
		r.Header.Get("POLY_PASSPHRASE") != "pass" || r.Header.Get("POLY_ADDRESS") != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
		t.Errorf("bad auth headers on %s %s: %v", r.Method, r.URL.Path, r.Header)
	}
	if time.Since(time.Unix(ts, 0)) > time.Minute {
		t.Errorf("stale timestamp %d", ts)
	}
}

func TestPlaceOrderRequestAndResponse(t *testing.T) {
	var posted map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			io.WriteString(w, `{"version":2}`)
		case "/tick-size":
			io.WriteString(w, `{"minimum_tick_size":0.01}`)
		case "/neg-risk":
			io.WriteString(w, `{"neg_risk":false}`)
		case "/order":
			body, _ := io.ReadAll(r.Body)
			checkAuth(t, r, body)
			json.Unmarshal(body, &posted)
			io.WriteString(w, `{"success":true,"errorMsg":"","orderID":"0xabc","status":"live"}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})

	resp, err := c.PlaceOrder(&types.OrderRequest{MarketID: testToken, Side: "buy", Price: 0.523, Quantity: 10, Type: "GTC"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success || resp.OrderID != "0xabc" || resp.Status != "live" {
		t.Errorf("response = %+v", resp)
	}

	o := posted["order"].(map[string]any)
	if posted["owner"] != "key-123" || posted["orderType"] != "GTC" || posted["postOnly"] != false {
		t.Errorf("envelope = %v", posted)
	}
	if o["side"] != "BUY" || o["makerAmount"] != "5200000" || o["takerAmount"] != "10000000" ||
		o["tokenId"] != testToken || o["signatureType"] != float64(0) || !strings.HasPrefix(o["signature"].(string), "0x") {
		t.Errorf("order = %v", o)
	}
	if _, isNumber := o["salt"].(float64); !isNumber {
		t.Errorf("salt must be a JSON number, got %T", o["salt"])
	}
}

func TestPlaceOrderRejectionsAndMapping(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			calls++
			io.WriteString(w, `{"version":2}`)
		case "/tick-size":
			io.WriteString(w, `{"minimum_tick_size":"0.001"}`)
		case "/neg-risk":
			io.WriteString(w, `{"neg_risk":true}`)
		case "/order":
			io.WriteString(w, `{"success":false,"errorMsg":"not enough balance / allowance","orderID":"","status":""}`)
		}
	})

	resp, err := c.PlaceOrder(&types.OrderRequest{MarketID: testToken, Side: "sell", Price: 0.5, Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success || resp.Error != "not enough balance / allowance" {
		t.Errorf("rejection = %+v", resp)
	}

	// Invalid orders are rejected locally without a request.
	resp, _ = c.PlaceOrder(&types.OrderRequest{MarketID: testToken, Side: "buy", Price: 0.9995, Quantity: 1})
	if resp.Success || !strings.Contains(resp.Error, "outside") {
		t.Errorf("out-of-range price = %+v", resp)
	}
	resp, _ = c.PlaceOrder(&types.OrderRequest{MarketID: testToken, Side: "buy", Price: 0.5, Quantity: 1, Type: "GTD"})
	if resp.Success || !strings.Contains(resp.Error, "GTD") {
		t.Errorf("GTD = %+v", resp)
	}
	if calls != 1 {
		t.Errorf("version fetched %d times, want once (cached)", calls)
	}
}

func TestVersionMismatchRetries(t *testing.T) {
	posts := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			io.WriteString(w, `{"version":`+strconv.Itoa(2+min(posts, 1))+`}`)
		case "/tick-size":
			io.WriteString(w, `{"minimum_tick_size":0.01}`)
		case "/neg-risk":
			io.WriteString(w, `{"neg_risk":false}`)
		case "/order":
			posts++
			if posts == 1 {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"order_version_mismatch"}`)
				return
			}
			io.WriteString(w, `{"success":true,"orderID":"0x1","status":"matched"}`)
		}
	})
	resp, err := c.PlaceOrder(&types.OrderRequest{MarketID: testToken, Side: "buy", Price: 0.5, Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if posts != 2 || resp.Status != "filled" {
		t.Errorf("posts = %d, resp = %+v", posts, resp)
	}
	if v, _ := c.Version(); v != 3 {
		t.Errorf("version after mismatch = %d, want 3", v)
	}
}

func TestPaginationAndPositions(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data/orders":
			checkAuth(t, r, nil)
			if r.URL.Query().Get("next_cursor") == firstCursor {
				io.WriteString(w, `{"data":[{"id":"o1","asset_id":"1","side":"BUY","price":"0.4","original_size":"10","size_matched":"0"}],"next_cursor":"Mg=="}`)
			} else {
				io.WriteString(w, `{"data":[{"id":"o2","asset_id":"1","side":"SELL","price":"0.6","original_size":"5","size_matched":"1"}],"next_cursor":"LTE="}`)
			}
		case "/data/trades":
			if r.URL.Query().Get("after") != "1760000000" {
				t.Errorf("after = %q", r.URL.Query().Get("after"))
			}
			io.WriteString(w, `{"data":[{"id":"t1","trader_side":"TAKER","maker_orders":[{"order_id":"m","matched_amount":"2"}]}],"next_cursor":"LTE="}`)
		case "/v2/positions":
			if r.URL.Query().Get("user") != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
				t.Errorf("user = %q", r.URL.Query().Get("user"))
			}
			if r.URL.Query().Get("cursor") == "" {
				io.WriteString(w, `{"data":[{"token_id":"1","current_size":12.5,"avg_price":0.42}],"pagination":{"has_more":true,"next_cursor":"c2"}}`)
			} else {
				io.WriteString(w, `{"data":[{"token_id":"2","current_size":3,"avg_price":0.1}],"pagination":{"has_more":false,"next_cursor":null}}`)
			}
		}
	})

	orders, err := c.GetOpenOrders()
	if err != nil || len(orders) != 2 || orders[1].ID != "o2" {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
	trades, err := c.GetTrades(time.Unix(1760000000, 0))
	if err != nil || len(trades) != 1 || trades[0].MakerOrders[0].MatchedAmount != "2" {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
	pos, err := c.GetPositions()
	if err != nil || len(pos) != 2 || pos[0].CurrentSize != 12.5 || pos[1].TokenID != "2" {
		t.Fatalf("positions = %+v, %v", pos, err)
	}
}

func TestTradingRequiresKey(t *testing.T) {
	c := NewClient(&config.Config{API: config.APIConfig{RESTEndpoint: "http://unused"}})
	if _, err := c.PlaceOrder(&types.OrderRequest{}); err == nil {
		t.Error("order placed without a key")
	}
}
