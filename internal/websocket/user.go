package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seeker/polymarket-bot/internal/api"
)

// UserEvent is one message from the authenticated user channel: exactly one
// of Trade, Order or Reconnected is set.
type UserEvent struct {
	Trade *api.Trade
	Order *OrderEvent
	// Reconnected is set after the feed reconnects. Events missed while it was
	// down are not replayed, so the receiver should resync over REST.
	Reconnected bool
}

// OrderEvent is a placement, update or cancellation of one of our orders.
type OrderEvent struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	Market       string `json:"market"`
	AssetID      string `json:"asset_id"`
	Side         string `json:"side"`
	OriginalSize string `json:"original_size"`
	SizeMatched  string `json:"size_matched"`
	Price        string `json:"price"`
	Type         string `json:"type"`   // PLACEMENT, UPDATE, CANCELLATION
	Status       string `json:"status"` // LIVE, MATCHED, DELAYED, UNMATCHED, CANCELED
}

// UserClient streams this account's order and trade updates from
// wss://ws-subscriptions-clob.polymarket.com/ws/user, reconnecting as needed.
type UserClient struct {
	url        string
	apiKey     string
	secret     string
	passphrase string

	events chan UserEvent
	errs   chan error

	mu   sync.Mutex
	conn *websocket.Conn
}

// NewUserClient creates a user-channel client with the account's API credentials.
func NewUserClient(url, apiKey, secret, passphrase string) *UserClient {
	return &UserClient{
		url:        url,
		apiKey:     apiKey,
		secret:     secret,
		passphrase: passphrase,
		events:     make(chan UserEvent, 256),
		errs:       make(chan error, 16),
	}
}

// Events delivers order and trade updates.
func (c *UserClient) Events() <-chan UserEvent { return c.events }

// Errors delivers connection problems; the client keeps retrying on its own.
func (c *UserClient) Errors() <-chan error { return c.errs }

// Connect dials once so startup fails fast on bad credentials or network
// problems, then keeps the feed running in the background until ctx ends.
func (c *UserClient) Connect(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	go c.run(ctx, conn)
	return nil
}

func (c *UserClient) dial() (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("user feed: connect: %w", err)
	}
	sub := map[string]any{
		"type": "user",
		"auth": map[string]string{"apiKey": c.apiKey, "secret": c.secret, "passphrase": c.passphrase},
	}
	if err := conn.WriteJSON(sub); err != nil {
		conn.Close()
		return nil, fmt.Errorf("user feed: subscribe: %w", err)
	}
	return conn, nil
}

func (c *UserClient) run(ctx context.Context, conn *websocket.Conn) {
	go func() { // close the socket when we're told to stop, unblocking the read
		<-ctx.Done()
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
	}()

	for attempt := 0; ; {
		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()

		err := c.serve(ctx, conn)
		conn.Close()
		if ctx.Err() != nil {
			return
		}
		c.report(fmt.Errorf("user feed disconnected: %w", err))

		// Reconnect with capped backoff until it works or we're stopped.
		for {
			attempt++
			delay := time.Duration(min(attempt, 6)) * 5 * time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if conn, err = c.dial(); err == nil {
				attempt = 0
				c.emit(ctx, UserEvent{Reconnected: true})
				break
			}
			c.report(err)
		}
	}
}

// serve reads one connection until it fails, pinging every 10s as required.
func (c *UserClient) serve(ctx context.Context, conn *websocket.Conn) error {
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				c.mu.Lock()
				err := conn.WriteMessage(websocket.TextMessage, []byte("PING"))
				c.mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(40 * time.Second)) // PONGs arrive every 10s
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		for _, ev := range parseUserMessage(msg) {
			c.emit(ctx, ev)
		}
	}
}

// parseUserMessage decodes one frame, which may hold a single event or an array.
func parseUserMessage(msg []byte) []UserEvent {
	if len(msg) == 0 || string(msg) == "PONG" {
		return nil
	}
	var raws []json.RawMessage
	if msg[0] == '[' {
		if json.Unmarshal(msg, &raws) != nil {
			return nil
		}
	} else {
		raws = []json.RawMessage{msg}
	}

	var out []UserEvent
	for _, raw := range raws {
		var head struct {
			EventType string `json:"event_type"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch head.EventType {
		case "trade":
			var t api.Trade
			if json.Unmarshal(raw, &t) == nil && t.ID != "" {
				out = append(out, UserEvent{Trade: &t})
			}
		case "order":
			var o OrderEvent
			if json.Unmarshal(raw, &o) == nil && o.ID != "" {
				out = append(out, UserEvent{Order: &o})
			}
		}
	}
	return out
}

// emit blocks rather than dropping: a lost fill would corrupt position tracking.
func (c *UserClient) emit(ctx context.Context, ev UserEvent) {
	select {
	case c.events <- ev:
	case <-ctx.Done():
	}
}

func (c *UserClient) report(err error) {
	select {
	case c.errs <- err:
	default:
	}
}
