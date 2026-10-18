package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

// Client talks to the Polymarket CLOB REST API (and the Data API for positions).
type Client struct {
	cfg        *config.APIConfig
	httpClient *http.Client
	baseURL    string
	dataURL    string
	apiKey     string
	apiSecret  string
	passphrase string

	// Set by EnableTrading; nil in paper mode.
	signer  *Signer
	builder *OrderBuilder

	mu        sync.Mutex
	tickSizes map[string]string
	negRisk   map[string]bool
	version   int // exchange order version from GET /version; 0 = not fetched yet
}

// NewClient creates an API client. It can read public data straight away;
// call EnableTrading before placing orders.
func NewClient(cfg *config.Config) *Client {
	transport := &http.Transport{
		MaxIdleConns:        cfg.API.MaxIdleConns,
		MaxConnsPerHost:     cfg.API.MaxConnsPerHost,
		DisableKeepAlives:   !cfg.API.KeepaliveEnabled,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.API.TimeoutMS) * time.Millisecond,
	}

	return &Client{
		cfg:        &cfg.API,
		httpClient: httpClient,
		baseURL:    strings.TrimRight(cfg.API.RESTEndpoint, "/"),
		dataURL:    strings.TrimRight(cfg.API.DataAPIURL, "/"),
		apiKey:     cfg.API.APIKey,
		apiSecret:  cfg.API.APISecret,
		passphrase: cfg.API.Passphrase,
		tickSizes:  make(map[string]string),
		negRisk:    make(map[string]bool),
	}
}

// EnableTrading loads the wallet key and prepares order signing.
func (c *Client) EnableTrading() error {
	signer, err := NewSigner(c.cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("api.private_key: %w", err)
	}
	if _, err := decodeSecret(c.apiSecret); err != nil {
		return err
	}
	var funder Address
	if c.cfg.FunderAddress != "" {
		if funder, err = ParseAddress(c.cfg.FunderAddress); err != nil {
			return fmt.Errorf("api.funder_address: %w", err)
		}
	}
	builder, err := NewOrderBuilder(signer, funder, c.cfg.SignatureType)
	if err != nil {
		return err
	}
	c.signer, c.builder = signer, builder
	return nil
}

// SignerAddress is the address of the private key (sent as POLY_ADDRESS).
func (c *Client) SignerAddress() string { return c.signer.Address().String() }

// MakerAddress is the wallet that holds funds and positions.
func (c *Client) MakerAddress() string { return c.builder.Maker().String() }

// APIKey identifies this account's orders and fills ("owner" in API payloads).
func (c *Client) APIKey() string { return c.apiKey }

// PlaceOrder submits a single order to the API
func (c *Client) PlaceOrder(req *types.OrderRequest) (*types.OrderResponse, error) {
	responses, err := c.PlaceOrders([]*types.OrderRequest{req})
	if err != nil {
		return nil, err
	}

	if len(responses) == 0 {
		return nil, fmt.Errorf("no response received")
	}

	return responses[0], nil
}

// orderPayload is one entry of POST /order(s).
type orderPayload struct {
	DeferExec bool         `json:"deferExec"`
	PostOnly  bool         `json:"postOnly"`
	Order     *SignedOrder `json:"order"`
	Owner     string       `json:"owner"`
	OrderType string       `json:"orderType"`
}

// apiOrderResponse is the CLOB's reply for one posted order.
type apiOrderResponse struct {
	Success  bool   `json:"success"`
	ErrorMsg string `json:"errorMsg"`
	OrderID  string `json:"orderID"`
	Status   string `json:"status"` // live, matched, delayed, unmatched
}

// PlaceOrders signs and submits orders. An order the exchange rejects comes
// back with Success false and Error set; err is only for request failures.
func (c *Client) PlaceOrders(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
	if c.builder == nil {
		return nil, fmt.Errorf("trading not enabled (no private key loaded)")
	}

	resps, err := c.postOrders(requests)
	if err != nil && strings.Contains(err.Error(), "order_version_mismatch") {
		// The exchange moved to a new order version: refresh it and sign again.
		c.mu.Lock()
		c.version = 0
		c.mu.Unlock()
		resps, err = c.postOrders(requests)
	}
	return resps, err
}

func (c *Client) postOrders(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
	out := make([]*types.OrderResponse, len(requests))
	payloads := make([]orderPayload, 0, len(requests))
	index := make([]int, 0, len(requests)) // payload position -> request position

	version, err := c.Version()
	if err != nil {
		return nil, err
	}
	for i, req := range requests {
		p, err := c.buildPayload(req, version)
		if err != nil {
			out[i] = &types.OrderResponse{Success: false, Error: err.Error()}
			continue
		}
		payloads = append(payloads, p)
		index = append(index, i)
	}
	if len(payloads) == 0 {
		return out, nil
	}

	var body []byte
	endpoint := "/order"
	if len(payloads) == 1 {
		body, err = json.Marshal(payloads[0])
	} else {
		endpoint = "/orders"
		body, err = json.Marshal(payloads)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to encode order: %w", err)
	}

	respData, err := c.signedRequest("POST", endpoint, nil, body)
	if err != nil {
		return nil, err
	}

	var results []apiOrderResponse
	if err := json.Unmarshal(respData, &results); err != nil {
		var single apiOrderResponse
		if err := json.Unmarshal(respData, &single); err != nil {
			return nil, fmt.Errorf("failed to parse order response: %w", err)
		}
		results = []apiOrderResponse{single}
	}
	for j, i := range index {
		if j >= len(results) {
			out[i] = &types.OrderResponse{Success: false, Error: "no response for order"}
			continue
		}
		r := results[j]
		resp := &types.OrderResponse{Success: r.Success && r.ErrorMsg == "", OrderID: r.OrderID, Error: r.ErrorMsg}
		switch r.Status {
		case "matched":
			resp.Status = "filled" // fully matched on arrival; the fill arrives on the user feed
		case "unmatched":
			resp.Status = "unmatched" // FAK/FOK that didn't fill; nothing is left open
		default:
			resp.Status = r.Status
		}
		out[i] = resp
	}
	return out, nil
}

func (c *Client) buildPayload(req *types.OrderRequest, version int) (orderPayload, error) {
	orderType, err := normalizeOrderType(req.Type)
	if err != nil {
		return orderPayload{}, err
	}
	tick, err := c.TickSize(req.MarketID)
	if err != nil {
		return orderPayload{}, err
	}
	negRisk := false
	if version != 3 {
		if negRisk, err = c.NegRisk(req.MarketID); err != nil {
			return orderPayload{}, err
		}
	}
	signed, err := c.builder.Build(OrderParams{
		TokenID:  req.MarketID,
		Side:     req.Side,
		Price:    req.Price,
		Size:     req.Quantity,
		TickSize: tick,
		NegRisk:  negRisk,
		Version:  version,
	})
	if err != nil {
		return orderPayload{}, err
	}
	return orderPayload{Order: signed, Owner: c.apiKey, OrderType: orderType}, nil
}

// normalizeOrderType maps config values onto the CLOB's order types.
func normalizeOrderType(t string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "", "GTC":
		return "GTC", nil
	case "IOC", "FAK": // immediate-or-cancel is called fill-and-kill on Polymarket
		return "FAK", nil
	case "FOK":
		return "FOK", nil
	case "GTD":
		return "", fmt.Errorf("GTD orders need an expiration, which the bot doesn't set; use GTC")
	}
	return "", fmt.Errorf("unknown order type %q (want GTC, FAK or FOK)", t)
}

// CancelOrder cancels an open order.
func (c *Client) CancelOrder(orderID string) error {
	body, _ := json.Marshal(map[string]string{"orderID": orderID})
	_, err := c.signedRequest("DELETE", "/order", nil, body)
	return err
}

// OpenOrder is a resting order as returned by GET /data/orders.
type OpenOrder struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Owner        string `json:"owner"`
	Market       string `json:"market"`
	AssetID      string `json:"asset_id"`
	Side         string `json:"side"`
	OriginalSize string `json:"original_size"`
	SizeMatched  string `json:"size_matched"`
	Price        string `json:"price"`
	OrderType    string `json:"order_type"`
	CreatedAt    int64  `json:"created_at"`
}

// GetOpenOrders returns all of this account's open orders.
func (c *Client) GetOpenOrders() ([]OpenOrder, error) {
	var all []OpenOrder
	err := c.paginate("/data/orders", nil, func(raw json.RawMessage) error {
		var page []OpenOrder
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		all = append(all, page...)
		return nil
	})
	return all, err
}

// MakerOrder is one resting order filled by a trade.
type MakerOrder struct {
	OrderID       string `json:"order_id"`
	Owner         string `json:"owner"`
	MakerAddress  string `json:"maker_address"`
	MatchedAmount string `json:"matched_amount"`
	Price         string `json:"price"`
	FeeRateBps    string `json:"fee_rate_bps"`
	AssetID       string `json:"asset_id"`
	Outcome       string `json:"outcome"`
	Side          string `json:"side"`
}

// Trade is a match involving this account, from GET /data/trades or the
// user websocket. Price, size and side are from the taker's point of view.
type Trade struct {
	ID           string       `json:"id"`
	TakerOrderID string       `json:"taker_order_id"`
	Market       string       `json:"market"`
	AssetID      string       `json:"asset_id"`
	Side         string       `json:"side"`
	Size         string       `json:"size"`
	FeeRateBps   string       `json:"fee_rate_bps"`
	Price        string       `json:"price"`
	Status       string       `json:"status"`
	MatchTime    string       `json:"match_time"`
	Outcome      string       `json:"outcome"`
	Owner        string       `json:"owner"`
	MakerAddress string       `json:"maker_address"`
	MakerOrders  []MakerOrder `json:"maker_orders"`
	TraderSide   string       `json:"trader_side"` // TAKER or MAKER
}

// GetTrades returns this account's trades matched after the given time.
func (c *Client) GetTrades(after time.Time) ([]Trade, error) {
	q := url.Values{}
	if !after.IsZero() {
		q.Set("after", strconv.FormatInt(after.Unix(), 10))
	}
	var all []Trade
	err := c.paginate("/data/trades", q, func(raw json.RawMessage) error {
		var page []Trade
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		all = append(all, page...)
		return nil
	})
	return all, err
}

const (
	firstCursor = "MA=="
	endCursor   = "LTE="
)

// paginate walks a cursor-paginated authenticated endpoint.
func (c *Client) paginate(path string, q url.Values, page func(json.RawMessage) error) error {
	cursor := firstCursor
	for i := 0; cursor != endCursor && cursor != ""; i++ {
		if i > 1000 {
			return fmt.Errorf("%s: too many pages", path)
		}
		params := url.Values{}
		for k, v := range q {
			params[k] = v
		}
		params.Set("next_cursor", cursor)
		data, err := c.signedRequest("GET", path, params, nil)
		if err != nil {
			return err
		}
		var env struct {
			Data       json.RawMessage `json:"data"`
			NextCursor string          `json:"next_cursor"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("%s: failed to parse response: %w", path, err)
		}
		if err := page(env.Data); err != nil {
			return fmt.Errorf("%s: failed to parse page: %w", path, err)
		}
		cursor = env.NextCursor
	}
	return nil
}

// Position is a holding reported by the Data API.
type Position struct {
	TokenID      string  `json:"token_id"`
	ConditionID  string  `json:"condition_id"`
	CurrentSize  float64 `json:"current_size"`
	AvgPrice     float64 `json:"avg_price"`
	CurrentPrice float64 `json:"current_price"`
	RealizedPnL  float64 `json:"realized_pnl"`
	Status       string  `json:"status"`
	Outcome      string  `json:"outcome"`
	Title        string  `json:"title"`
	NegativeRisk bool    `json:"negative_risk"`
}

// GetPositions returns the open positions held by the maker wallet.
func (c *Client) GetPositions() ([]Position, error) {
	var all []Position
	q := url.Values{"user": {c.MakerAddress()}, "limit": {"500"}}
	for i := 0; ; i++ {
		if i > 100 {
			return nil, fmt.Errorf("positions: too many pages")
		}
		var page struct {
			Data       []Position `json:"data"`
			Pagination struct {
				HasMore    bool    `json:"has_more"`
				NextCursor *string `json:"next_cursor"`
			} `json:"pagination"`
		}
		if err := c.publicGet(c.dataURL, "/v2/positions", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if !page.Pagination.HasMore || page.Pagination.NextCursor == nil {
			return all, nil
		}
		q = url.Values{"user": {c.MakerAddress()}, "cursor": {*page.Pagination.NextCursor}}
	}
}

// TickSize returns the market's minimum price increment, e.g. "0.01".
func (c *Client) TickSize(tokenID string) (string, error) {
	c.mu.Lock()
	t, ok := c.tickSizes[tokenID]
	c.mu.Unlock()
	if ok {
		return t, nil
	}
	var r struct {
		MinimumTickSize json.Number `json:"minimum_tick_size"`
	}
	if err := c.publicGet(c.baseURL, "/tick-size", url.Values{"token_id": {tokenID}}, &r); err != nil {
		return "", fmt.Errorf("tick size for %s: %w", tokenID, err)
	}
	t = r.MinimumTickSize.String()
	c.mu.Lock()
	c.tickSizes[tokenID] = t
	c.mu.Unlock()
	return t, nil
}

// NegRisk reports whether the token trades on the neg-risk exchange.
func (c *Client) NegRisk(tokenID string) (bool, error) {
	c.mu.Lock()
	v, ok := c.negRisk[tokenID]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	var r struct {
		NegRisk bool `json:"neg_risk"`
	}
	if err := c.publicGet(c.baseURL, "/neg-risk", url.Values{"token_id": {tokenID}}, &r); err != nil {
		return false, fmt.Errorf("neg-risk flag for %s: %w", tokenID, err)
	}
	c.mu.Lock()
	c.negRisk[tokenID] = r.NegRisk
	c.mu.Unlock()
	return r.NegRisk, nil
}

// Version returns the exchange order version the CLOB currently accepts.
func (c *Client) Version() (int, error) {
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	if v != 0 {
		return v, nil
	}
	var r struct {
		Version int `json:"version"`
	}
	if err := c.publicGet(c.baseURL, "/version", nil, &r); err != nil {
		return 0, fmt.Errorf("order version: %w", err)
	}
	if r.Version == 0 {
		r.Version = 2
	}
	c.mu.Lock()
	c.version = r.Version
	c.mu.Unlock()
	return r.Version, nil
}

// signedRequest makes an L2-authenticated request. The signature covers the
// path and the exact body bytes sent, but not the query string.
func (c *Client) signedRequest(method, path string, q url.Values, body []byte) ([]byte, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("trading not enabled (no private key loaded)")
	}
	timestamp := time.Now().Unix()
	sig, err := l2Signature(c.apiSecret, timestamp, method, path, body)
	if err != nil {
		return nil, err
	}

	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("POLY_ADDRESS", c.signer.Address().String())
	req.Header.Set("POLY_SIGNATURE", sig)
	req.Header.Set("POLY_TIMESTAMP", strconv.FormatInt(timestamp, 10))
	req.Header.Set("POLY_API_KEY", c.apiKey)
	req.Header.Set("POLY_PASSPHRASE", c.passphrase)
	return c.do(req)
}

func (c *Client) publicGet(base, path string, q url.Values, out any) error {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	data, err := c.do(req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// HealthCheck verifies API connectivity using the /time endpoint
func (c *Client) HealthCheck() error {
	req, err := http.NewRequest("GET", c.baseURL+"/time", nil)
	if err != nil {
		return err
	}
	if _, err := c.do(req); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	return nil
}
