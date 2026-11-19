package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// maxCallbackDataBytes is the Bot API limit for an inline button's
// callback_data.
const maxCallbackDataBytes = 64

// InlineButton is one inline-keyboard button. CallbackData must be at most
// 64 bytes (a Bot API limit, validated before any request).
type InlineButton struct{ Text, CallbackData string }

// BotClient talks to the interactive parts of the Bot API: messages with
// inline keyboards, callback answers and long-polling getUpdates. It shares
// the publisher's netguard validation, error classification and token
// redaction. Each method makes exactly one HTTP attempt.
type BotClient struct {
	baseURL string
	token   string
	timeout time.Duration
	client  *http.Client
}

// NewBotClient builds a BotClient. An empty baseURL means DefaultBaseURL; a
// non-positive timeout means the package default. timeout bounds ordinary
// calls; getUpdates adds its long-poll time on top.
func NewBotClient(baseURL, token string, timeout time.Duration) *BotClient {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	timeout = netguard.TimeoutOr(timeout, defaultTimeout)
	tr := netguard.NewTransport(netguard.AllowPrivate)
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 90 * time.Second
	return &BotClient{
		baseURL: baseURL,
		token:   token,
		timeout: timeout,
		// No client-level timeout: each call carries its own context deadline
		// because getUpdates legitimately outlives an ordinary call.
		client: &http.Client{Transport: tr, CheckRedirect: netguard.CheckRedirect},
	}
}

func keyboard(rows [][]InlineButton) (map[string]any, error) {
	out := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]string, 0, len(row))
		for _, b := range row {
			if len(b.CallbackData) > maxCallbackDataBytes {
				return nil, fmt.Errorf("telegram: callback_data is %d bytes, max %d", len(b.CallbackData), maxCallbackDataBytes)
			}
			r = append(r, map[string]string{"text": b.Text, "callback_data": b.CallbackData})
		}
		out = append(out, r)
	}
	return map[string]any{"inline_keyboard": out}, nil
}

// Send sends text with an optional inline keyboard and returns the message id.
func (b *BotClient) Send(ctx context.Context, chatID int64, text string, rows [][]InlineButton) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": text}
	if len(rows) > 0 {
		kb, err := keyboard(rows)
		if err != nil {
			return 0, err
		}
		body["reply_markup"] = kb
	}
	return b.sendBody(ctx, body)
}

// SendForceReply sends text that makes the client open a reply to it.
func (b *BotClient) SendForceReply(ctx context.Context, chatID int64, text string) (int64, error) {
	return b.sendBody(ctx, map[string]any{
		"chat_id": chatID, "text": text,
		"reply_markup": map[string]any{"force_reply": true},
	})
}

func (b *BotClient) sendBody(ctx context.Context, body map[string]any) (int64, error) {
	raw, err := b.call(ctx, "sendMessage", body, b.timeout)
	if err != nil {
		return 0, err
	}
	var res sendMessageResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, &appout.AmbiguousError{Err: fmt.Errorf("telegram: parse result: %w", err)}
	}
	return res.MessageID, nil
}

// Edit replaces a message's text. nil rows removes its inline keyboard.
func (b *BotClient) Edit(ctx context.Context, chatID, messageID int64, text string, rows [][]InlineButton) error {
	kb, err := keyboard(rows)
	if err != nil {
		return err
	}
	_, err = b.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text, "reply_markup": kb,
	}, b.timeout)
	return err
}

// AnswerCallback acknowledges a button press, optionally showing text.
func (b *BotClient) AnswerCallback(ctx context.Context, callbackID, text string) error {
	body := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		body["text"] = text
	}
	_, err := b.call(ctx, "answerCallbackQuery", body, b.timeout)
	return err
}

type rawUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		From      struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text    string `json:"text"`
		ReplyTo *struct {
			Text string `json:"text"`
		} `json:"reply_to_message"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Data    string `json:"data"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// GetUpdates long-polls for updates from offset for up to timeout. Updates
// that are neither a message nor a callback come back with only UpdateID
// set, so the caller still advances past them.
func (b *BotClient) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]appout.TelegramUpdate, error) {
	raw, err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int64(timeout / time.Second),
		"allowed_updates": []string{"message", "callback_query"},
	}, timeout+b.timeout)
	if err != nil {
		return nil, err
	}
	var in []rawUpdate
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("telegram: parse updates: %w", err)
	}
	res := make([]appout.TelegramUpdate, 0, len(in))
	for _, r := range in {
		u := appout.TelegramUpdate{UpdateID: r.UpdateID}
		switch {
		case r.Message != nil:
			m := r.Message
			u.ChatID, u.UserID, u.MessageID, u.Text = m.Chat.ID, m.From.ID, m.MessageID, m.Text
			if m.ReplyTo != nil {
				u.ReplyToText = m.ReplyTo.Text
			}
		case r.CallbackQuery != nil:
			c := r.CallbackQuery
			u.UserID, u.CallbackID, u.CallbackData = c.From.ID, c.ID, c.Data
			if c.Message != nil {
				u.ChatID, u.MessageID = c.Message.Chat.ID, c.Message.MessageID
			}
		}
		res = append(res, u)
	}
	return res, nil
}

// call POSTs body to the named method and returns the result field. Errors
// are classified like the publisher's and never contain the token.
func (b *BotClient) call(ctx context.Context, method string, body map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if _, err := netguard.ValidateURL(b.baseURL, netguard.AllowPrivate); err != nil {
		return nil, &appout.PermanentError{Err: fmt.Errorf("telegram: base_url: %w", err)}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("telegram: marshal request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqURL := fmt.Sprintf("%s/bot%s/%s", b.baseURL, b.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("telegram: build request: %w", netguard.RedactError(err, b.token))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: %s failed: %w", method, netguard.RedactError(err, b.token))
	}
	defer resp.Body.Close()

	data, err := netguard.ReadBody(resp.Body, responseLimitBytes)
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("telegram: %s read response: %w", method, netguard.RedactError(err, b.token))
	}
	var parsed apiResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("telegram: %s parse response (http %d): %w", method, resp.StatusCode, err)
	}
	if !parsed.OK {
		return nil, classifyError(resp.StatusCode, parsed)
	}
	return parsed.Result, nil
}
