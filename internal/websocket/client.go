package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/internal/logging"
	"github.com/seeker/polymarket-bot/pkg/types"
)

var logWS = logging.NewTopic("WS", logging.Cyan)

// Client manages WebSocket connection to Polymarket
type Client struct {
	cfg       *config.WebSocketConfig
	url       string
	markets   []string
	conn      *websocket.Conn
	msgChan   chan *types.MarketData
	errChan   chan error
	mu        sync.RWMutex
	running   bool
	reconnect bool
	ctx       context.Context
	cancel    context.CancelFunc

	quotes  map[string]*quote // latest top of book per subscribed token
	targets map[string]bool   // tokens we subscribed to; the feed also sends their siblings
}

// quote is the latest best bid/ask and trade price for one token.
type quote struct {
	bid, ask, last float64
}

// NewClient creates a new WebSocket client
func NewClient(cfg *config.Config) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		cfg:       &cfg.WebSocket,
		url:       cfg.API.WebSocketURL,
		markets:   cfg.Markets.TargetMarketIDs,
		msgChan:   make(chan *types.MarketData, cfg.WebSocket.MessageBufferSize),
		errChan:   make(chan error, 100),
		reconnect: cfg.WebSocket.ReconnectEnabled,
		ctx:       ctx,
		cancel:    cancel,
		quotes:    make(map[string]*quote),
		targets:   targetSet(cfg.Markets.TargetMarketIDs),
	}
}

// Connect establishes WebSocket connection and subscribes to markets
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("client already running")
	}

	dialer := websocket.Dialer{
		ReadBufferSize:  c.cfg.ReadBufferSize,
		WriteBufferSize: c.cfg.WriteBufferSize,
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.conn = conn
	c.running = true

	// Subscribe to target markets
	if err := c.subscribe(); err != nil {
		c.conn.Close()
		c.running = false
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	// Start read pump
	go c.readPump()

	// Start ping/pong handler
	go c.pingHandler()

	return nil
}

// subscribe sends subscription messages for target markets
func (c *Client) subscribe() error {
	// Per Polymarket WSS Quickstart, send a single subscribe frame with assets_ids
	msg := map[string]interface{}{
		"type":       "market",
		"assets_ids": c.markets,
	}

	if raw, err := json.Marshal(msg); err == nil {
		logWS.Debugf("Subscribe payload: %s", raw)
	}

	if err := c.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("failed to subscribe to markets: %w", err)
	}

	return nil
}

// readPump continuously reads messages from WebSocket
func (c *Client) readPump() {
	defer func() {
		c.mu.Lock()
		c.running = false
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()

		// Auto-reconnect if enabled
		if c.reconnect {
			c.handleReconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				c.errChan <- fmt.Errorf("read error: %w", err)
				return
			}

			// Parse and process message
			c.processMessage(message)
		}
	}
}

// marketEvent covers the market channel's event types: book, price_change,
// last_trade_price, best_bid_ask and tick_size_change.
type marketEvent struct {
	EventType    string        `json:"event_type"`
	AssetID      string        `json:"asset_id"`
	Bids         []priceLevel  `json:"bids"`
	Asks         []priceLevel  `json:"asks"`
	PriceChanges []priceChange `json:"price_changes"`
	Price        string        `json:"price"`
	Size         string        `json:"size"`
	BestBid      string        `json:"best_bid"`
	BestAsk      string        `json:"best_ask"`
	Timestamp    string        `json:"timestamp"` // unix milliseconds
}

type priceLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type priceChange struct {
	AssetID string `json:"asset_id"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

// processMessage decodes one frame (an event or an array of events) and
// publishes a price update for each subscribed token whose quote changed.
func (c *Client) processMessage(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "PONG" {
		return
	}
	logWS.Debugf("Message (%d bytes): %.512s", len(trimmed), trimmed)

	var events []marketEvent
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &events); err != nil {
			c.errChan <- fmt.Errorf("failed to parse market message: %w", err)
			return
		}
	} else {
		var ev marketEvent
		if err := json.Unmarshal(trimmed, &ev); err != nil {
			c.errChan <- fmt.Errorf("failed to parse market message: %w", err)
			return
		}
		events = []marketEvent{ev}
	}

	for _, ev := range events {
		for _, id := range c.applyEvent(&ev) {
			c.publish(id, parseMillis(ev.Timestamp))
		}
	}
}

// applyEvent updates the quotes and returns the tokens it changed.
func (c *Client) applyEvent(ev *marketEvent) []string {
	kind := ev.EventType
	switch { // book snapshots on subscribe arrive without event_type
	case kind == "" && (ev.Bids != nil || ev.Asks != nil):
		kind = "book"
	case kind == "" && ev.PriceChanges != nil:
		kind = "price_change"
	}

	switch kind {
	case "book":
		q := c.quote(ev.AssetID)
		if q == nil {
			return nil
		}
		q.bid, q.ask = 0, 0
		for _, l := range ev.Bids {
			q.bid = max(q.bid, parseFloat(l.Price))
		}
		for _, l := range ev.Asks {
			if p := parseFloat(l.Price); p > 0 && (q.ask == 0 || p < q.ask) {
				q.ask = p
			}
		}
		return []string{ev.AssetID}
	case "price_change":
		var changed []string
		for _, pc := range ev.PriceChanges {
			if q := c.quote(pc.AssetID); q != nil {
				q.bid, q.ask = parseFloat(pc.BestBid), parseFloat(pc.BestAsk)
				changed = append(changed, pc.AssetID)
			}
		}
		return changed
	case "best_bid_ask":
		if q := c.quote(ev.AssetID); q != nil {
			q.bid, q.ask = parseFloat(ev.BestBid), parseFloat(ev.BestAsk)
			return []string{ev.AssetID}
		}
	case "last_trade_price":
		if q := c.quote(ev.AssetID); q != nil {
			q.last = parseFloat(ev.Price)
			return []string{ev.AssetID}
		}
	}
	return nil // tick_size_change and anything new: nothing to price
}

// quote returns the state for a subscribed token, or nil for others.
func (c *Client) quote(assetID string) *quote {
	if !c.targets[assetID] {
		return nil
	}
	q := c.quotes[assetID]
	if q == nil {
		q = &quote{}
		c.quotes[assetID] = q
	}
	return q
}

// publish sends the token's current quote to the strategy. Price is the
// bid/ask midpoint, or the last trade when one side of the book is empty.
func (c *Client) publish(assetID string, ts time.Time) {
	q := c.quotes[assetID]
	price := q.last
	if q.bid > 0 && q.ask > 0 {
		price = (q.bid + q.ask) / 2
	}
	if price <= 0 {
		return
	}
	data := &types.MarketData{
		MarketID:    assetID,
		Price:       price,
		BidPrice:    q.bid,
		AskPrice:    q.ask,
		Timestamp:   ts,
		LastUpdated: time.Now(),
	}

	// Non-blocking send to avoid slowdown
	select {
	case c.msgChan <- data:
	default:
		c.errChan <- fmt.Errorf("message channel full, dropping message for market %s", assetID)
	}
}

func targetSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseMillis(s string) time.Time {
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ms)
}

// pingHandler sends periodic pings to keep connection alive
func (c *Client) pingHandler() {
	interval := time.Duration(c.cfg.PingIntervalSec) * time.Second
	if interval <= 0 || interval > 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				// The market channel expects a text PING at least every 10s
				if err := c.conn.WriteMessage(websocket.TextMessage, []byte("PING")); err != nil {
					c.errChan <- fmt.Errorf("ping failed: %w", err)
				}
			}
			c.mu.Unlock()
		}
	}
}

// handleReconnect attempts to reconnect with exponential backoff
func (c *Client) handleReconnect() {
	attempts := 0
	maxAttempts := c.cfg.ReconnectMaxAttempts
	baseDelay := time.Duration(c.cfg.ReconnectDelaySec) * time.Second

	for attempts < maxAttempts {
		attempts++
		delay := baseDelay * time.Duration(attempts)

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(delay):
			if err := c.Connect(); err != nil {
				c.errChan <- fmt.Errorf("reconnect attempt %d failed: %w", attempts, err)
				continue
			}
			return
		}
	}

	c.errChan <- fmt.Errorf("max reconnect attempts reached")
}

// Messages returns the channel for receiving market data
func (c *Client) Messages() <-chan *types.MarketData {
	return c.msgChan
}

// Errors returns the channel for receiving errors
func (c *Client) Errors() <-chan error {
	return c.errChan
}

// IsRunning checks if the client is connected and running
func (c *Client) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// Close gracefully shuts down the WebSocket connection
func (c *Client) Close() error {
	c.cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		// Send close message
		err := c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		if err != nil {
			return err
		}

		return c.conn.Close()
	}

	return nil
}
