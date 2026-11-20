// Package feishuconnector is a minimal, self-contained Feishu/Lark bot connector,
// embedded from the standalone feishu-connector so pigo can bind a bot and
// receive messages without a public webhook:
// register a Handler, Start(), and Send() messages/cards/files. It wraps the
// official larksuite/oapi-sdk-go SDK and owns the plumbing every custom-app
// bot needs (webhook or websocket transport, event verification, message
// dedup, @mention gating, card-action routing) so an agent only has to
// implement its own Handler.
//
// Minimal usage:
//
//	b, err := feishuconnector.New(feishuconnector.Config{
//		AppID:             os.Getenv("FEISHU_APP_ID"),
//		AppSecret:         os.Getenv("FEISHU_APP_SECRET"),
//		VerificationToken: os.Getenv("FEISHU_VERIFICATION_TOKEN"),
//		Mode:              "websocket",
//	})
//	b.OnMessage(func(ctx context.Context, msg feishuconnector.Message) {
//		b.SendText(ctx, msg.ChatID, "echo: "+msg.Text)
//	})
//	b.Start(ctx)
package feishuconnector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Config configures a Bot. AppID/AppSecret come from the Feishu/Lark
// developer console (custom app credentials).
type Config struct {
	AppID     string
	AppSecret string

	// Domain selects the platform: "feishu" (default, feishu.cn) or "lark"
	// (lark.com, the international product).
	Domain string

	// Mode selects the event transport: "websocket" (default — no public
	// endpoint needed, works behind NAT/firewalls) or "webhook" (exposes an
	// HTTP endpoint Feishu calls; requires VerificationToken).
	Mode string

	// VerificationToken is the event-subscription token from the developer
	// console. Required for webhook mode (used to authenticate inbound
	// calls); used opportunistically for websocket mode's dispatcher too.
	VerificationToken string

	// WebhookPort/WebhookPath configure the HTTP listener in webhook mode.
	// Defaults: 8080, "/feishu/event".
	WebhookPort int
	WebhookPath string

	// RequireMention, if true, ignores group-chat messages that don't
	// @-mention the bot. Direct messages are always delivered.
	RequireMention bool

	Logger *slog.Logger
}

func (c Config) domain() string {
	if strings.EqualFold(strings.TrimSpace(c.Domain), "lark") {
		return "lark"
	}
	return "feishu"
}

func (c Config) mode() string {
	if c.Mode == "" {
		return "websocket"
	}
	return c.Mode
}

// Bot is a running Feishu/Lark connection. Create with New, register a
// Handler with OnMessage, then Start.
type Bot struct {
	cfg      Config
	logger   *slog.Logger
	handler  Handler
	cancel   context.CancelFunc
	client   *lark.Client
	wsClient *larkws.Client

	seenMu sync.Mutex
	seen   map[string]bool
}

// New validates cfg and constructs a Bot. It does not connect yet — call
// Start for that.
func New(cfg Config) (*Bot, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, fmt.Errorf("feishu: AppID and AppSecret are required")
	}
	if cfg.mode() == "webhook" && strings.TrimSpace(cfg.VerificationToken) == "" {
		return nil, fmt.Errorf("feishu: webhook mode needs VerificationToken set — refusing to expose an unauthenticated event endpoint")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = "/feishu/event"
	}
	return &Bot{
		cfg:    cfg,
		logger: logger.With("component", "feishu"),
		seen:   make(map[string]bool),
	}, nil
}

// OnMessage registers the callback invoked for every inbound text message
// and card-action click. Call it before Start; it is not safe to change
// concurrently with a running Bot.
func (b *Bot) OnMessage(h Handler) {
	b.handler = h
}

// Start connects to Feishu/Lark (websocket long-connection or a local
// webhook HTTP server, per Config.Mode) and begins delivering events to the
// registered Handler. It returns once the transport goroutine is launched;
// use ctx cancellation or Stop to shut down.
func (b *Bot) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)
	switch b.cfg.mode() {
	case "webhook":
		go b.runWebhook(ctx)
	default:
		go b.runWebSocket(ctx)
	}
	return nil
}

// Stop closes the active connection. Safe to call once; a Bot is not
// reusable after Stop.
func (b *Bot) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}
	if b.wsClient != nil {
		b.wsClient.Close()
	}
	return nil
}

func (b *Bot) emit(ctx context.Context, msg Message) {
	if b.handler == nil {
		return
	}
	go b.handler(ctx, msg)
}

func (b *Bot) markSeen(eventID string) bool {
	if eventID == "" {
		return false
	}
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	if b.seen == nil {
		b.seen = make(map[string]bool)
	}
	if b.seen[eventID] {
		return true
	}
	b.seen[eventID] = true
	if len(b.seen) > 10000 {
		b.seen = make(map[string]bool)
		b.seen[eventID] = true
	}
	return false
}

func (b *Bot) verificationTokenValid(token string) bool {
	return b.cfg.VerificationToken == "" || token == b.cfg.VerificationToken
}

func (b *Bot) sdkClient() *lark.Client {
	if b.client != nil {
		return b.client
	}
	opts := []lark.ClientOptionFunc{
		lark.WithLogLevel(larkcore.LogLevelError),
		lark.WithSource("feishu-connector"),
	}
	if b.cfg.domain() == "lark" {
		opts = append(opts, lark.WithOpenBaseUrl(lark.LarkBaseUrl), lark.WithOAuthBaseUrl(lark.OAuthBaseUrlLark))
	}
	b.client = lark.NewClient(b.cfg.AppID, b.cfg.AppSecret, opts...)
	return b.client
}

func (b *Bot) newEventDispatcher() *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher(b.cfg.VerificationToken, "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func cardActionChatType(raw string) ChatType {
	if ChatType(raw) == ChatGroup {
		return ChatGroup
	}
	if ChatType(raw) == ChatDM {
		return ChatDM
	}
	return ChatGroup
}
