package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// defaultPollTimeout is how long one getUpdates call waits for new messages.
const defaultPollTimeout = 30 * time.Second

// retryDelay is the pause after a failed poll before trying again.
var retryDelay = 5 * time.Second

// Controller is what the bot's commands read and change. The trading bot
// implements it; each method returns the reply text.
type Controller interface {
	Status() string
	PnL() string
	Orders() string
	Pause() string
	Resume() string
}

// Logger receives the bot's own diagnostics.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

// Config configures a Bot.
type Config struct {
	Token         string
	ChatIDs       []int64       // the only chats that may use commands; all get notifications. Empty: only tell people their chat ID
	AlertCooldown time.Duration // minimum gap between notifications with the same key
	APIURL        string        // defaults to DefaultAPIURL
	PollTimeout   time.Duration // defaults to 30s; tests shorten it
	Proxy         string        // optional socks5:// or http:// proxy for Telegram traffic only
}

var commands = []BotCommand{
	{Command: "status", Description: "Mode, uptime, feed and order stats"},
	{Command: "pnl", Description: "Equity, P&L and open positions"},
	{Command: "orders", Description: "Orders still working"},
	{Command: "pause", Description: "Stop placing new orders"},
	{Command: "resume", Description: "Start placing orders again"},
	{Command: "help", Description: "List commands"},
}

// outgoing is a queued message; chatID 0 means every configured chat.
type outgoing struct {
	chatID int64
	text   string
}

// Bot answers commands from allowed chats and pushes notifications to them.
// A nil *Bot is valid and does nothing, so callers don't need to check
// whether Telegram is enabled.
type Bot struct {
	client   *Client
	chats    []int64
	allowed  map[int64]bool
	ctrl     Controller
	log      Logger
	cooldown time.Duration
	poll     time.Duration
	limiter  *rate.Limiter
	started  time.Time

	mu       sync.Mutex
	closed   bool
	lastSent map[string]time.Time

	queue      chan outgoing
	sendCtx    context.Context
	stopSend   context.CancelFunc
	senderDone chan struct{}
}

// New checks the config and creates a bot. Call Start to connect.
func New(cfg Config, ctrl Controller, log Logger) (*Bot, error) {
	if cfg.Token == "" {
		return nil, errors.New("telegram: bot token is empty")
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.PollTimeout <= 0 || cfg.PollTimeout > defaultPollTimeout {
		cfg.PollTimeout = defaultPollTimeout
	}
	allowed := make(map[int64]bool, len(cfg.ChatIDs))
	for _, id := range cfg.ChatIDs {
		allowed[id] = true
	}
	client := NewClient(cfg.APIURL, cfg.Token)
	if cfg.Proxy != "" {
		if err := client.SetProxy(cfg.Proxy); err != nil {
			return nil, err
		}
	}
	sendCtx, stop := context.WithCancel(context.Background())
	return &Bot{
		client:   client,
		chats:    cfg.ChatIDs,
		allowed:  allowed,
		ctrl:     ctrl,
		log:      log,
		cooldown: cfg.AlertCooldown,
		poll:     cfg.PollTimeout,
		// Telegram allows about one message a second per chat; allow short bursts.
		limiter:    rate.NewLimiter(1, 5),
		lastSent:   make(map[string]time.Time),
		queue:      make(chan outgoing, 100),
		sendCtx:    sendCtx,
		stopSend:   stop,
		senderDone: make(chan struct{}),
	}, nil
}

// Start checks the token, registers the command menu and starts answering
// commands until ctx ends. Commands sent while the bot was offline are ignored.
func (b *Bot) Start(ctx context.Context) error {
	name, err := b.client.GetMe(ctx)
	if err != nil {
		b.stopSend()
		close(b.senderDone)
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("telegram rejected the bot token (check bot_token): %w", err)
		}
		return fmt.Errorf("can't reach Telegram (if it's blocked on your network, set telegram.proxy): %w", err)
	}
	if err := b.client.SetMyCommands(ctx, commands); err != nil {
		b.log.Warnf("Couldn't register the command menu: %v", err)
	}
	b.started = time.Now()
	if len(b.chats) == 0 {
		b.log.Warnf("Connected as @%s, but telegram.chat_ids is empty: send /start to @%s to get your chat ID", name, name)
	} else {
		b.log.Infof("Connected as @%s, sending to %d chat(s)", name, len(b.chats))
	}
	go b.sendLoop()
	go b.pollLoop(ctx)
	return nil
}

// Notify sends text to every configured chat without blocking. Messages
// with the same non-empty key are sent at most once per cooldown, so a
// repeating alert doesn't flood the chat. Dropped if the queue is full.
func (b *Bot) Notify(key, text string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.due(key) {
		b.enqueue(outgoing{text: text})
	}
}

// due reports whether a message with this key may go out now, and if so
// starts its cooldown. Empty keys are always due. Caller holds b.mu.
func (b *Bot) due(key string) bool {
	if key == "" {
		return true
	}
	if last, ok := b.lastSent[key]; ok && time.Since(last) < b.cooldown {
		return false
	}
	b.lastSent[key] = time.Now()
	return true
}

// enqueue adds a message to the send queue, dropping it when full so
// trading never waits on Telegram. Caller holds b.mu.
func (b *Bot) enqueue(m outgoing) {
	select {
	case b.queue <- m:
	default:
		b.log.Warnf("Send queue full; dropped a message")
	}
}

// reply answers one chat; key works as in Notify.
func (b *Bot) reply(chatID int64, key, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.due(key) {
		b.enqueue(outgoing{chatID: chatID, text: text})
	}
}

// Close sends what is still queued, waiting at most timeout.
func (b *Bot) Close(timeout time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.queue)
	b.mu.Unlock()

	select {
	case <-b.senderDone:
	case <-time.After(timeout):
		b.log.Warnf("Gave up sending queued messages after %s", timeout)
	}
	b.stopSend()
}

func (b *Bot) sendLoop() {
	defer close(b.senderDone)
	for m := range b.queue {
		targets := b.chats
		if m.chatID != 0 {
			targets = []int64{m.chatID}
		}
		for _, id := range targets {
			b.send(id, m.text)
		}
	}
}

// send delivers one message, retrying once if Telegram asks us to slow down.
func (b *Bot) send(chatID int64, text string) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.limiter.Wait(b.sendCtx); err != nil {
			return
		}
		err := b.client.SendMessage(b.sendCtx, chatID, text)
		if err == nil {
			return
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && attempt == 0 {
			select {
			case <-time.After(apiErr.RetryAfter):
				continue
			case <-b.sendCtx.Done():
				return
			}
		}
		b.log.Warnf("Send to chat %d failed: %v", chatID, err)
		return
	}
}

func (b *Bot) pollLoop(ctx context.Context) {
	var offset int64
	failing := false
	for {
		updates, err := b.client.GetUpdates(ctx, offset, b.poll)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failing { // log once per outage, not every retry
				b.log.Warnf("Polling failed, retrying every %s: %v", retryDelay, err)
				failing = true
			}
			select {
			case <-time.After(retryDelay):
				continue
			case <-ctx.Done():
				return
			}
		}
		if failing {
			b.log.Infof("Polling recovered")
			failing = false
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(u)
		}
	}
}

func (b *Bot) handle(u Update) {
	m := u.Message
	if m == nil || m.Text == "" {
		return
	}
	if time.Unix(m.Date, 0).Before(b.started.Add(-time.Second)) {
		return // sent while the bot was offline
	}
	if !b.allowed[m.Chat.ID] {
		from := ""
		if m.From != nil && m.From.Username != "" {
			from = " from @" + m.From.Username
		}
		b.log.Warnf("Ignored %q%s in chat %d (not in telegram.chat_ids)", m.Text, from, m.Chat.ID)
		// Answered once per cooldown, so strangers can't use the bot to spam.
		b.reply(m.Chat.ID, fmt.Sprintf("stranger:%d", m.Chat.ID),
			fmt.Sprintf("This bot is private. Your chat ID is %d; add it to telegram.chat_ids in config.yml to use it.", m.Chat.ID))
		return
	}
	b.reply(m.Chat.ID, "", b.dispatch(parseCommand(m.Text)))
}

func (b *Bot) dispatch(cmd string) string {
	switch cmd {
	case "status":
		return b.ctrl.Status()
	case "pnl":
		return b.ctrl.PnL()
	case "orders":
		return b.ctrl.Orders()
	case "pause":
		return b.ctrl.Pause()
	case "resume":
		return b.ctrl.Resume()
	case "start", "help":
		return helpText()
	default:
		return "Unknown command.\n\n" + helpText()
	}
}

// parseCommand turns "/Status@MyBot extra" into "status"; non-commands give "".
func parseCommand(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	cmd, _, _ := strings.Cut(fields[0][1:], "@")
	return strings.ToLower(cmd)
}

func helpText() string {
	var sb strings.Builder
	sb.WriteString("Commands:\n")
	for _, c := range commands {
		fmt.Fprintf(&sb, "/%s - %s\n", c.Command, c.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}
