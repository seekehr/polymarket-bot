package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "123456:SECRET-token"

// fakeAPI is a minimal Bot API: it serves queued updates once and records
// sent messages.
type fakeAPI struct {
	mu      sync.Mutex
	updates []Update
	sent    []sentMessage
}

type sentMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
			http.Error(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any = true
		switch method {
		case "getMe":
			result = User{Username: "test_bot"}
		case "getUpdates":
			result = f.updates
			f.updates = nil
		case "sendMessage":
			var m sentMessage
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Errorf("bad sendMessage body: %v", err)
			}
			f.sent = append(f.sent, m)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	})
}

func (f *fakeAPI) push(chatID int64, text string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, Update{
		UpdateID: int64(len(f.updates) + 1),
		Message:  &Message{Date: at.Unix(), Text: text, Chat: Chat{ID: chatID}},
	})
}

func (f *fakeAPI) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

type fakeController struct {
	mu    sync.Mutex
	calls []string
}

func (c *fakeController) call(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	return name + " reply"
}

func (c *fakeController) Status() string { return c.call("status") }
func (c *fakeController) PnL() string    { return c.call("pnl") }
func (c *fakeController) Orders() string { return c.call("orders") }
func (c *fakeController) Pause() string  { return c.call("pause") }
func (c *fakeController) Resume() string { return c.call("resume") }

type nopLogger struct{}

func (nopLogger) Infof(string, ...any) {}
func (nopLogger) Warnf(string, ...any) {}

func startBot(t *testing.T, api *fakeAPI, ctrl Controller) (*Bot, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	b, err := New(Config{Token: testToken, ChatIDs: []int64{42, 43}, AlertCooldown: time.Hour, APIURL: srv.URL, PollTimeout: 50 * time.Millisecond}, ctrl, nopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	b.limiter.SetLimit(1000)
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return b, cancel
}

// waitFor polls until cond holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCommandsFromAllowedChat(t *testing.T) {
	api := &fakeAPI{}
	ctrl := &fakeController{}
	b, cancel := startBot(t, api, ctrl)
	defer cancel()

	api.push(42, "/status", time.Now())
	api.push(42, "/Pause@test_bot now", time.Now())
	waitFor(t, "two replies", func() bool { return len(api.messages()) == 2 })
	b.Close(time.Second)

	got := api.messages()
	if got[0] != (sentMessage{42, "status reply"}) || got[1] != (sentMessage{42, "pause reply"}) {
		t.Errorf("replies = %+v", got)
	}
}

func TestStrangerGetsChatIDOnceAndNoAccess(t *testing.T) {
	api := &fakeAPI{}
	ctrl := &fakeController{}
	b, cancel := startBot(t, api, ctrl)
	defer cancel()

	api.push(999, "/pause", time.Now())
	api.push(999, "/pause", time.Now())
	api.push(42, "/help", time.Now()) // marks when the stranger's messages are done
	waitFor(t, "help reply", func() bool {
		for _, m := range api.messages() {
			if m.ChatID == 42 {
				return true
			}
		}
		return false
	})
	b.Close(time.Second)

	var toStranger []sentMessage
	for _, m := range api.messages() {
		if m.ChatID == 999 {
			toStranger = append(toStranger, m)
		}
	}
	if len(toStranger) != 1 || !strings.Contains(toStranger[0].Text, "999") {
		t.Errorf("stranger got %+v, want one message with their chat ID", toStranger)
	}
	if len(ctrl.calls) != 0 {
		t.Errorf("stranger reached the controller: %v", ctrl.calls)
	}
}

func TestIgnoresMessagesSentWhileOffline(t *testing.T) {
	api := &fakeAPI{}
	ctrl := &fakeController{}
	b, cancel := startBot(t, api, ctrl)
	defer cancel()

	api.push(42, "/pause", time.Now().Add(-time.Hour))
	api.push(42, "/status", time.Now())
	waitFor(t, "status reply", func() bool { return len(api.messages()) == 1 })
	b.Close(time.Second)

	if len(ctrl.calls) != 1 || ctrl.calls[0] != "status" {
		t.Errorf("calls = %v, want only status", ctrl.calls)
	}
}

func TestNotifyBroadcastsAndCoolsDown(t *testing.T) {
	api := &fakeAPI{}
	b, cancel := startBot(t, api, &fakeController{})
	defer cancel()

	b.Notify("lat", "slow 1")
	b.Notify("lat", "slow 2") // within cooldown: dropped
	b.Notify("", "fill")
	b.Notify("", "fill")
	b.Close(time.Second)

	var texts []string
	for _, m := range api.messages() {
		texts = append(texts, fmt.Sprintf("%d:%s", m.ChatID, m.Text))
	}
	want := "42:slow 1,43:slow 1,42:fill,43:fill,42:fill,43:fill"
	if strings.Join(texts, ",") != want {
		t.Errorf("sent %v, want %s", texts, want)
	}
}

func TestNilBotIsNoop(t *testing.T) {
	var b *Bot
	b.Notify("", "hi")
	b.Close(time.Second)
}

func TestParseCommand(t *testing.T) {
	cases := map[string]string{
		"/status":           "status",
		"/PnL@my_bot":       "pnl",
		"  /orders extra  ": "orders",
		"hello":             "",
		"":                  "",
	}
	for in, want := range cases {
		if got := parseCommand(in); got != want {
			t.Errorf("parseCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorsHideToken(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", testToken) // nothing listens here
	_, err := c.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestProxyCarriesTelegramTraffic(t *testing.T) {
	api := &fakeAPI{}
	var proxied atomic.Int32
	// A forward proxy receives absolute URLs; this one answers them as the API.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "telegram.invalid" {
			proxied.Add(1)
		}
		api.handler(t).ServeHTTP(w, r)
	}))
	defer proxy.Close()

	c := NewClient("http://telegram.invalid", testToken) // unreachable without the proxy
	if err := c.SetProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	name, err := c.GetMe(context.Background())
	if err != nil || name != "test_bot" {
		t.Fatalf("GetMe = %q, %v", name, err)
	}
	if proxied.Load() != 1 {
		t.Errorf("proxy saw %d requests, want 1", proxied.Load())
	}
}

func TestSetProxyRejectsBadURLs(t *testing.T) {
	for _, p := range []string{"127.0.0.1:1080", "ftp://host:21", "socks5://"} {
		if err := NewClient(DefaultAPIURL, testToken).SetProxy(p); err == nil {
			t.Errorf("SetProxy(%q) accepted", p)
		}
	}
	if err := NewClient(DefaultAPIURL, testToken).SetProxy("http://user:hunter2@:8080"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error for proxy without host should hide the password: %v", err)
	}
}

func TestBadTokenFailsStart(t *testing.T) {
	srv := httptest.NewServer((&fakeAPI{}).handler(t))
	defer srv.Close()
	b, err := New(Config{Token: "wrong", ChatIDs: []int64{1}, APIURL: srv.URL}, &fakeController{}, nopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err == nil {
		t.Error("Start succeeded with a bad token")
	}
	b.Close(time.Second) // must not hang
}
