package telegram_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/httpfault"
)

const botToken = "123456:" + httpfault.Secret

func sampleArticle() article.Article {
	return article.Article{ID: "a1", Title: "Hello", CanonicalURL: "https://example.com/a"}
}

func TestPublisher_TimesOutOnHangingServer(t *testing.T) {
	pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: httpfault.Hanging(t).URL, Timeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := pub.Publish(context.Background(), sampleArticle())
	if err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call took %v", time.Since(start))
	}
	if strings.Contains(err.Error(), httpfault.Secret) {
		t.Fatalf("err leaks the token: %v", err)
	}
}

func TestPublisher_ErrorsNeverContainToken(t *testing.T) {
	for _, base := range []string{
		httpfault.Closing(t).URL,
		"http://127.0.0.1:1",
		"ftp://api.example.com",
		"https://user:" + httpfault.Secret + "@api.example.com",
	} {
		pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: base, Timeout: 2 * time.Second})
		_, err := pub.Publish(context.Background(), sampleArticle())
		if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
			t.Fatalf("base %q: err = %v, must be non-nil and secret-free", base, err)
		}
	}
}

func TestPublisher_InvalidBaseURLIsPermanent(t *testing.T) {
	for _, base := range []string{"ftp://api.example.com", "http://169.254.169.254"} {
		pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: base, Timeout: time.Second})
		_, err := pub.Publish(context.Background(), sampleArticle())
		var permanent *appout.PermanentError
		if !errors.As(err, &permanent) || !errors.Is(err, netguard.ErrDisallowedURL) {
			t.Fatalf("base %q: err = %v, want permanent ErrDisallowedURL", base, err)
		}
	}
}

func TestPublisher_RejectsOversizedBody(t *testing.T) {
	pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: httpfault.Oversized(t, 2<<20).URL, Timeout: 5 * time.Second})
	_, err := pub.Publish(context.Background(), sampleArticle())
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestPublisher_StopsRedirectLoop(t *testing.T) {
	srv, hits := httpfault.RedirectLoop(t)
	pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: srv.URL, Timeout: 2 * time.Second})
	_, err := pub.Publish(context.Background(), sampleArticle())
	if err == nil || strings.Contains(err.Error(), httpfault.Secret) {
		t.Fatalf("err = %v, want a secret-free error", err)
	}
	if hits.Load() != netguard.MaxRedirects+1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestPublisher_SendsNeutralUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken, BaseURL: srv.URL, Timeout: 2 * time.Second})
	if _, err := pub.Publish(context.Background(), sampleArticle()); err != nil {
		t.Fatal(err)
	}
	if ua != netguard.DefaultUserAgent {
		t.Fatalf("User-Agent = %q", ua)
	}
}

func TestPublisher_ZeroTimeoutGetsDefault(t *testing.T) {
	pub := telegram.New(telegram.Config{ChannelID: "@example", BotToken: botToken})
	if got := telegram.ClientTimeoutForTest(pub); got <= 0 {
		t.Fatalf("timeout = %v, want a positive default", got)
	}
}
