package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

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
	pool      sync.Pool // Object pool for MarketData structs
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
		pool: sync.Pool{
			New: func() interface{} {
				return &types.MarketData{}
			},
		},
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
		fmt.Printf("WebSocket subscribe payload: %s\n", string(raw))
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

// processMessage decodes WebSocket message and pushes to channel
func (c *Client) processMessage(raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}

	// Helpful debug output (truncated to avoid log spam)
	const maxLogLen = 512
	logSnippet := trimmed
	if len(logSnippet) > maxLogLen {
		logSnippet = logSnippet[:maxLogLen]
	}
	fmt.Printf("WebSocket message (%d bytes): %s\n", len(trimmed), string(logSnippet))

	// The Polymarket feed can return an array of updates; handle both array and object.
	if trimmed[0] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			c.errChan <- fmt.Errorf("failed to unmarshal array message: %w", err)
			return
		}
		for _, item := range arr {
			c.handleDataItem(item)
		}
		return
	}

	var wsMsg types.WebSocketMessage
	if err := json.Unmarshal(trimmed, &wsMsg); err != nil {
		c.errChan <- fmt.Errorf("failed to unmarshal message: %w", err)
		return
	}

	if m, ok := wsMsg.Data.(map[string]interface{}); ok {
		c.handleDataItem(m)
		return
	}

	// If Data is not an object, drop with notice.
	c.errChan <- fmt.Errorf("unexpected data format: %T", wsMsg.Data)
}

// handleDataItem converts a generic map into MarketData and publishes it.
func (c *Client) handleDataItem(item map[string]interface{}) {
	// Get struct from pool to reduce GC pressure
	data := c.pool.Get().(*types.MarketData)
	defer c.pool.Put(data)

	dataBytes, err := json.Marshal(item)
	if err != nil {
		c.errChan <- fmt.Errorf("failed to marshal data item: %w", err)
		return
	}

	if err := json.Unmarshal(dataBytes, data); err != nil {
		c.errChan <- fmt.Errorf("failed to unmarshal market data: %w", err)
		return
	}

	data.LastUpdated = time.Now()

	// Non-blocking send to avoid slowdown
	select {
	case c.msgChan <- data:
	default:
		c.errChan <- fmt.Errorf("message channel full, dropping message for market %s", data.MarketID)
	}
}

// pingHandler sends periodic pings to keep connection alive
func (c *Client) pingHandler() {
	ticker := time.NewTicker(time.Duration(c.cfg.PingIntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				deadline := time.Now().Add(time.Duration(c.cfg.PongTimeoutSec) * time.Second)
				if err := c.conn.WriteControl(websocket.PingMessage, []byte{}, deadline); err != nil {
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
