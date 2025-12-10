package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/seeker/polymarket-bot/internal/config"
	"github.com/seeker/polymarket-bot/pkg/types"
)

// Client handles HTTP requests to Polymarket CLOB API
type Client struct {
	cfg        *config.APIConfig
	httpClient *http.Client
	baseURL    string
	apiKey     string
	apiSecret  string
	passphrase string
}

// NewClient creates a new API client
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
		baseURL:    cfg.API.RESTEndpoint,
		apiKey:     cfg.API.APIKey,
		apiSecret:  cfg.API.APISecret,
		passphrase: cfg.API.Passphrase,
	}
}

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

// PlaceOrders submits multiple orders (batch)
func (c *Client) PlaceOrders(requests []*types.OrderRequest) ([]*types.OrderResponse, error) {
	endpoint := "/order"
	if len(requests) > 1 {
		endpoint = "/orders" // Batch endpoint
	}

	// Convert to API format
	payload, err := json.Marshal(requests)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make signed request
	respData, err := c.signedRequest("POST", endpoint, payload)
	if err != nil {
		return nil, err
	}

	// Parse response
	var responses []*types.OrderResponse
	if err := json.Unmarshal(respData, &responses); err != nil {
		// Try single response format
		var singleResp types.OrderResponse
		if err := json.Unmarshal(respData, &singleResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		responses = []*types.OrderResponse{&singleResp}
	}

	return responses, nil
}

// CancelOrder cancels an existing order
func (c *Client) CancelOrder(orderID string) error {
	endpoint := fmt.Sprintf("/order/%s", orderID)

	_, err := c.signedRequest("DELETE", endpoint, nil)
	return err
}

// GetOrder retrieves order status
func (c *Client) GetOrder(orderID string) (*types.Order, error) {
	endpoint := fmt.Sprintf("/order/%s", orderID)

	respData, err := c.signedRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var order types.Order
	if err := json.Unmarshal(respData, &order); err != nil {
		return nil, fmt.Errorf("failed to parse order: %w", err)
	}

	return &order, nil
}

// GetOpenOrders retrieves all open orders
func (c *Client) GetOpenOrders() ([]*types.Order, error) {
	endpoint := "/orders"

	respData, err := c.signedRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var orders []*types.Order
	if err := json.Unmarshal(respData, &orders); err != nil {
		return nil, fmt.Errorf("failed to parse orders: %w", err)
	}

	return orders, nil
}

// signedRequest makes a signed HTTP request to the API
func (c *Client) signedRequest(method, endpoint string, body []byte) ([]byte, error) {
	url := c.baseURL + endpoint
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Create request
	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Generate signature
	signature := c.generateSignature(timestamp, method, endpoint, body)

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("POLY-API-KEY", c.apiKey)
	req.Header.Set("POLY-SIGNATURE", signature)
	req.Header.Set("POLY-TIMESTAMP", timestamp)
	req.Header.Set("POLY-PASSPHRASE", c.passphrase)

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

// generateSignature creates HMAC-SHA256 signature for API authentication
func (c *Client) generateSignature(timestamp, method, endpoint string, body []byte) string {
	// Message format: timestamp + method + endpoint + body
	message := timestamp + method + endpoint
	if len(body) > 0 {
		message += string(body)
	}

	// Create HMAC
	h := hmac.New(sha256.New, []byte(c.apiSecret))
	h.Write([]byte(message))

	// Return hex-encoded signature
	return hex.EncodeToString(h.Sum(nil))
}

// GetMarketData retrieves current market data (for initial state)
func (c *Client) GetMarketData(marketID string) (*types.MarketData, error) {
	endpoint := fmt.Sprintf("/market/%s", marketID)

	respData, err := c.signedRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var data types.MarketData
	if err := json.Unmarshal(respData, &data); err != nil {
		return nil, fmt.Errorf("failed to parse market data: %w", err)
	}

	return &data, nil
}

// GetPositions retrieves current positions
func (c *Client) GetPositions() ([]*types.Position, error) {
	endpoint := "/positions"

	respData, err := c.signedRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var positions []*types.Position
	if err := json.Unmarshal(respData, &positions); err != nil {
		return nil, fmt.Errorf("failed to parse positions: %w", err)
	}

	return positions, nil
}

// HealthCheck verifies API connectivity using the /time endpoint
func (c *Client) HealthCheck() error {
	url := c.baseURL + "/time"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health check returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
