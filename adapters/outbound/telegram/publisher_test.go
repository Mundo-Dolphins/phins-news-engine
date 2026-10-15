package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestPublisher_PublishSendsExpectedRequestAndReturnsMessageID(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, err := w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	pub := telegram.New(telegram.Config{ChannelID: "-100", BotToken: "123:abc", BaseURL: server.URL, Timeout: 2 * time.Second})
	a := article.Article{ID: "a1", Title: "Hello", CanonicalURL: "https://example.com/a"}

	messageID, err := pub.Publish(context.Background(), a)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if messageID != "99" {
		t.Errorf("messageID = %q, want %q", messageID, "99")
	}
	if gotBody["chat_id"] != "-100" {
		t.Errorf("chat_id = %v, want -100", gotBody["chat_id"])
	}
	if gotBody["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", gotBody["parse_mode"])
	}
	if disable, ok := gotBody["disable_web_page_preview"].(bool); !ok || disable {
		t.Errorf("disable_web_page_preview = %v, want false (allow the preview)", gotBody["disable_web_page_preview"])
	}
}

func TestPublisher_PublishWrapsClassifiedErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden"}`)); err != nil {
			t.Fatalf("w.Write() error = %v", err)
		}
	}))
	defer server.Close()

	pub := telegram.New(telegram.Config{ChannelID: "-100", BotToken: "123:abc", BaseURL: server.URL, Timeout: 2 * time.Second})
	_, err := pub.Publish(context.Background(), article.Article{ID: "a1", Title: "T", CanonicalURL: "https://example.com/a"})

	var permanent *appout.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("err = %v, want *out.PermanentError", err)
	}
}

// TestPublisher_PublishFailsLocallyWithoutHTTPCallForOversizedMessage: an
// article whose title alone (never truncated) exceeds Telegram's UTF-16
// length limit must fail before any HTTP request is made, classified as a
// *appout.PermanentError so PublishOutbox discards it instead of retrying
// an article that can never fit.
func TestPublisher_PublishFailsLocallyWithoutHTTPCallForOversizedMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Publish made an HTTP request for a payload that was already known to be too long")
	}))
	defer server.Close()

	pub := telegram.New(telegram.Config{ChannelID: "-100", BotToken: "123:abc", BaseURL: server.URL, Timeout: 2 * time.Second})
	a := article.Article{
		ID:           "a1",
		Title:        strings.Repeat("word ", 1000),
		CanonicalURL: "https://example.com/a",
	}

	messageID, err := pub.Publish(context.Background(), a)

	var permanent *appout.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("err = %v, want *out.PermanentError", err)
	}
	if messageID != "" {
		t.Errorf("messageID = %q, want \"\"", messageID)
	}
}
