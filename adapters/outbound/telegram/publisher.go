package telegram

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DefaultBaseURL is the production Telegram Bot API host.
const DefaultBaseURL = "https://api.telegram.org"

// Config configures a Publisher.
type Config struct {
	// ChannelID is the Telegram chat/channel to post to (e.g.
	// "@channelname" or a numeric chat ID as a string).
	ChannelID string
	// BotToken authenticates with the Bot API. Always sourced from the
	// TELEGRAM_BOT_TOKEN environment variable by the caller — never from
	// YAML (see config.TelegramConfig's own doc comment).
	BotToken string
	// BaseURL overrides the Telegram API host; empty defaults to
	// DefaultBaseURL. Tests point this at an httptest.Server.
	BaseURL string
	// Timeout bounds each sendMessage call; zero means defaultTimeout.
	Timeout time.Duration
}

// defaultTimeout applies when Config.Timeout is not positive.
const defaultTimeout = 30 * time.Second

// Publisher sends accepted articles to a Telegram channel via the Bot
// API's sendMessage endpoint.
type Publisher struct {
	cfg    Config
	client *http.Client
}

// New builds a ready Publisher.
func New(cfg Config) *Publisher {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	tr := netguard.NewTransport(netguard.AllowPrivate)
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 90 * time.Second
	return &Publisher{
		cfg: cfg,
		client: &http.Client{
			Timeout:       netguard.TimeoutOr(cfg.Timeout, defaultTimeout),
			Transport:     tr,
			CheckRedirect: netguard.CheckRedirect,
		},
	}
}

var _ appout.Publisher = (*Publisher)(nil)

// Publish sends a to the configured Telegram channel and returns the sent
// message's ID. Media is out of scope for this phase — no article field
// carries an image URL yet — so every message is text plus
// Telegram's own link preview.
func (p *Publisher) Publish(ctx context.Context, a article.Article) (string, error) {
	text, err := buildMessage(a)
	if err != nil {
		// A payload Telegram would reject no matter how many times it's
		// retried: fail permanently before ever making an HTTP call, same
		// as classifyError does for a 400/403 response.
		return "", &appout.PermanentError{Err: fmt.Errorf("telegram: publish %s: %w", a.ID, err)}
	}

	body := map[string]any{
		"chat_id":                  p.cfg.ChannelID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	messageID, err := sendMessage(ctx, p.client, p.cfg.BaseURL, p.cfg.BotToken, body)
	if err != nil {
		return "", fmt.Errorf("telegram: publish %s: %w", a.ID, err)
	}
	return messageID, nil
}
