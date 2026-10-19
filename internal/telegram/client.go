// Package telegram is a small Telegram Bot API client and command bot for
// watching and steering the trading bot from a phone. It uses plain HTTPS long
// polling, so it needs no webhook, public address or extra dependencies.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIURL is the public Telegram Bot API.
const DefaultAPIURL = "https://api.telegram.org"

// maxMessageLen is Telegram's limit for one message's text.
const maxMessageLen = 4096

// Client calls the Bot API. Errors never contain the bot token.
type Client struct {
	base  string // https://api.telegram.org/bot<token>
	token string
	http  *http.Client
}

// NewClient creates a client for the given bot token. apiURL is normally
// DefaultAPIURL; tests point it at a local server.
func NewClient(apiURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(apiURL, "/") + "/bot" + token,
		token: token,
		// Long polls hold the request open for up to defaultPollTimeout.
		http: &http.Client{Timeout: defaultPollTimeout + 15*time.Second},
	}
}

// SetProxy sends this client's requests, and only these, through a proxy:
// socks5://host:port, socks5h://, http:// or https://, optionally with
// user:password@.
func (c *Client) SetProxy(proxy string) error {
	u, err := url.Parse(proxy)
	if err != nil {
		return fmt.Errorf("telegram: invalid proxy URL: %w", err)
	}
	switch u.Scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return fmt.Errorf("telegram: proxy %q must start with socks5://, socks5h://, http:// or https://", u.Redacted())
	}
	if u.Hostname() == "" || u.Port() == "" {
		return fmt.Errorf("telegram: proxy %q has no host:port", u.Redacted())
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyURL(u)
	c.http.Transport = t
	return nil
}

// Update is one incoming event. Only text messages are used.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a chat message.
type Message struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"` // unix seconds
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
}

// Chat identifies where a message came from and where replies go.
type Chat struct {
	ID int64 `json:"id"`
}

// User is the sender of a message.
type User struct {
	Username string `json:"username"`
}

// BotCommand is an entry in the bot's "/" command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// APIError is a Bot API error response.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429 Too Many Requests
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

type response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts params as JSON to a Bot API method and decodes the result into out.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()

	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		e := &APIError{Code: r.ErrorCode, Description: r.Description}
		if r.Parameters != nil {
			e.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
		}
		return e
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// redact strips the token from transport errors, whose messages include the
// request URL.
func (c *Client) redact(err error) error {
	if c.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.token, "<token>")
	msg = strings.ReplaceAll(msg, url.PathEscape(c.token), "<token>")
	return fmt.Errorf("%s", msg)
}

// GetMe checks the token and returns the bot's username.
func (c *Client) GetMe(ctx context.Context) (string, error) {
	var u User
	if err := c.call(ctx, "getMe", struct{}{}, &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

// GetUpdates long-polls for updates after offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

// SendMessage sends plain text to a chat, truncated to Telegram's limit.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	if len(text) > maxMessageLen {
		// Cut by bytes, then drop any rune split in half.
		text = strings.ToValidUTF8(text[:maxMessageLen-4], "") + "…"
	}
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}, nil)
}

// SetMyCommands fills the command menu shown when typing "/".
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
