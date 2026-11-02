package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultAlertDedupWindow is how long an alert with the same DedupKey is
	// suppressed after it was sent.
	DefaultAlertDedupWindow = 24 * time.Hour

	maxAlertTitleRunes   = 200
	maxAlertDetailsRunes = 1500
)

// AlerterConfig configures an Alerter.
type AlerterConfig struct {
	// ChatID is where alerts go: the news chat unless the host overrides it
	// (alerts.chat_id).
	ChatID string
	// BotToken authenticates with the Bot API; it is also scrubbed from every
	// alert text.
	BotToken string
	// BaseURL overrides the Telegram API host; empty defaults to DefaultBaseURL.
	BaseURL string
	// Timeout bounds each sendMessage call; zero means defaultTimeout.
	Timeout time.Duration
	// DedupWindow suppresses repeats of the same DedupKey; zero means
	// DefaultAlertDedupWindow.
	DedupWindow time.Duration
	// Secrets are extra values (app passwords, other tokens) removed from
	// alert text.
	Secrets []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Alerter sends operator alerts to a Telegram chat through the same
// sendMessage path as the news, clearly marked as internal. Alerts are plain
// text, never articles: they do not touch the outbox, deduplication or
// reconciliation. The dedup window is kept in memory, so a restart may repeat
// an alert once.
type Alerter struct {
	cfg    AlerterConfig
	client *http.Client

	mu   sync.Mutex
	sent map[string]time.Time // DedupKey -> when it was reserved
}

// NewAlerter builds a ready Alerter.
func NewAlerter(cfg AlerterConfig) *Alerter {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = DefaultAlertDedupWindow
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// Reuse the Publisher's hardened HTTP client.
	pub := New(Config{ChannelID: cfg.ChatID, BotToken: cfg.BotToken, BaseURL: cfg.BaseURL, Timeout: cfg.Timeout})
	return &Alerter{cfg: cfg, client: pub.client, sent: make(map[string]time.Time)}
}

var _ appout.Alerter = (*Alerter)(nil)

// Alert implements out.Alerter. A suppressed duplicate returns nil. When
// delivery fails the dedup reservation is dropped, so the next alert with the
// same key tries again.
func (al *Alerter) Alert(ctx context.Context, a appout.Alert) error {
	now := al.cfg.Now()
	if !al.reserve(a.DedupKey, now) {
		return nil
	}
	body := map[string]any{
		"chat_id":                  al.cfg.ChatID,
		"text":                     al.format(a),
		"disable_web_page_preview": true,
	}
	if _, err := sendMessage(ctx, al.client, al.cfg.BaseURL, al.cfg.BotToken, body); err != nil {
		al.release(a.DedupKey)
		return fmt.Errorf("telegram: alert %q: %w", a.DedupKey, err)
	}
	return nil
}

// reserve claims key for the dedup window and reports whether the caller may
// send. An empty key is never deduplicated.
func (al *Alerter) reserve(key string, now time.Time) bool {
	if key == "" {
		return true
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	for k, at := range al.sent {
		if now.Sub(at) >= al.cfg.DedupWindow {
			delete(al.sent, k)
		}
	}
	if _, dup := al.sent[key]; dup {
		return false
	}
	al.sent[key] = now
	return true
}

func (al *Alerter) release(key string) {
	if key == "" {
		return
	}
	al.mu.Lock()
	delete(al.sent, key)
	al.mu.Unlock()
}

// format renders the alert as plain text: "[alert] <severity> <source>", the
// title, then the details, scrubbed of credentials and truncated.
func (al *Alerter) format(a appout.Alert) string {
	severity := a.Severity
	if severity == "" {
		severity = appout.AlertWarning
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[alert] %s", severity)
	if src := al.clean(a.Source, 80); src != "" {
		fmt.Fprintf(&b, " · %s", src)
	}
	if title := al.clean(a.Title, maxAlertTitleRunes); title != "" {
		b.WriteString("\n" + title)
	}
	if details := al.clean(a.Details, maxAlertDetailsRunes); details != "" {
		b.WriteString("\n\n" + details)
	}
	return b.String()
}

var (
	// A Bot API token: <bot id>:<35-char secret>.
	botTokenText = regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{30,}\b`)
	// A Bluesky app password: four dash-separated groups of four.
	appPasswordText = regexp.MustCompile(`\b[a-z0-9]{4}(?:-[a-z0-9]{4}){3}\b`)
)

// clean scrubs credentials from s and truncates it to max runes.
func (al *Alerter) clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	secrets := append([]string{al.cfg.BotToken}, al.cfg.Secrets...)
	s = netguard.RedactError(errors.New(s), secrets...).Error()
	s = botTokenText.ReplaceAllString(s, "[REDACTED]")
	s = appPasswordText.ReplaceAllString(s, "[REDACTED]")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
