package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"

func TestBotClientSendWithKeyboard(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bot"+testToken+"/sendMessage") {
			t.Errorf("path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer srv.Close()

	bot := NewBotClient(srv.URL, testToken, 2*time.Second)
	id, err := bot.Send(context.Background(), 55, "hello", [][]InlineButton{{{Text: "Yes", CallbackData: "y:1"}, {Text: "No", CallbackData: "n:1"}}})
	if err != nil || id != 77 {
		t.Fatalf("Send() = %d, %v; want 77, nil", id, err)
	}
	if got["chat_id"] != float64(55) || got["text"] != "hello" {
		t.Errorf("body = %v", got)
	}
	rows := got["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	btn := rows[0].([]any)[1].(map[string]any)
	if btn["text"] != "No" || btn["callback_data"] != "n:1" {
		t.Errorf("button = %v", btn)
	}
}

func TestBotClientRejectsLongCallbackData(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()

	bot := NewBotClient(srv.URL, testToken, 2*time.Second)
	_, err := bot.Send(context.Background(), 1, "x", [][]InlineButton{{{Text: "a", CallbackData: strings.Repeat("a", 65)}}})
	if err == nil {
		t.Fatal("Send() error = nil, want error for 65-byte callback_data")
	}
	if calls.Load() != 0 {
		t.Errorf("requests = %d, want 0", calls.Load())
	}
	if _, err := bot.Send(context.Background(), 1, "x", [][]InlineButton{{{Text: "a", CallbackData: strings.Repeat("a", 64)}}}); err != nil && calls.Load() == 0 {
		t.Errorf("64-byte callback_data rejected: %v", err)
	}
}

func TestBotClientEditNilRowsRemovesKeyboard(t *testing.T) {
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	bot := NewBotClient(srv.URL, testToken, 2*time.Second)
	if err := bot.Edit(context.Background(), 5, 9, "done", nil); err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	if !strings.HasSuffix(path, "/editMessageText") {
		t.Errorf("path = %q", path)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	rm, _ := json.Marshal(m["reply_markup"])
	if string(rm) != `{"inline_keyboard":[]}` {
		t.Errorf("reply_markup = %s", rm)
	}
	if m["message_id"] != float64(9) || m["chat_id"] != float64(5) {
		t.Errorf("body = %s", body)
	}
}

func TestBotClientErrorsRedactToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused

	bot := NewBotClient(url, testToken, time.Second)
	_, err := bot.Send(context.Background(), 1, "x", nil)
	if err == nil {
		t.Fatal("Send() error = nil")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("error leaks token: %v", err)
	}
	if _, err := bot.GetUpdates(context.Background(), 0, time.Second); err == nil || strings.Contains(err.Error(), "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("GetUpdates error = %v", err)
	}
}

func TestBotClientGetUpdatesParses(t *testing.T) {
	var q map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &q)
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":7,"message":{"message_id":3,"from":{"id":11},"chat":{"id":22},"text":"hi","reply_to_message":{"text":"orig"}}},
			{"update_id":8,"callback_query":{"id":"cb1","from":{"id":11},"data":"y:1","message":{"message_id":4,"chat":{"id":22}}}},
			{"update_id":9,"edited_message":{"message_id":3}}]}`))
	}))
	defer srv.Close()

	bot := NewBotClient(srv.URL, testToken, 2*time.Second)
	ups, err := bot.GetUpdates(context.Background(), 7, 25*time.Second)
	if err != nil || len(ups) != 3 {
		t.Fatalf("GetUpdates() = %v, %v", ups, err)
	}
	if q["offset"] != float64(7) || q["timeout"] != float64(25) {
		t.Errorf("request = %v", q)
	}
	if u := ups[0]; u.UpdateID != 7 || u.UserID != 11 || u.ChatID != 22 || u.MessageID != 3 || u.Text != "hi" || u.ReplyToText != "orig" {
		t.Errorf("message update = %+v", u)
	}
	if u := ups[1]; u.CallbackID != "cb1" || u.CallbackData != "y:1" || u.ChatID != 22 || u.MessageID != 4 || u.UserID != 11 {
		t.Errorf("callback update = %+v", u)
	}
	if ups[2].UpdateID != 9 || ups[2].UserID != 0 {
		t.Errorf("unsupported update = %+v", ups[2])
	}
}
