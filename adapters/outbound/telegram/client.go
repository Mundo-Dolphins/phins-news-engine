package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// responseLimitBytes bounds how much of Telegram's response body is read
// into memory — Telegram's own responses are tiny (well under 1 KiB); this
// only guards against a misbehaving proxy or an unexpected huge payload.
const responseLimitBytes = 1 << 20 // 1 MiB

// apiResponse is the shape of every Telegram Bot API response.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type sendMessageResult struct {
	MessageID int64 `json:"message_id"`
}

// sendMessage POSTs body to the Telegram Bot API's sendMessage endpoint
// and returns the sent message's ID, or a classified error (see
// classifyError). It makes exactly one HTTP attempt — retrying is
// PublishOutbox's job (ADR 0007), not this client's.
//
// A transport failure, a response-body-read failure, or an envelope-parse
// failure returns *out.AmbiguousError instead of a plain error: in each of
// those cases whether Telegram actually received and processed the
// request is genuinely unknown (the request may have been sent and
// processed with the response lost in transit), so the caller must not
// treat it as a confirmed outcome — see ADR 0030.
//
// The bot token travels in the URL path, and net/http embeds the full URL
// in transport errors, so every error from request construction or the
// network call goes through netguard.RedactError with the token as an
// explicit secret before it can reach a log, article status_reason, outbox
// last_error or audit event. A base URL that is not http(s), carries
// credentials or points at a link-local address is a permanent error:
// retrying cannot fix configuration. Loopback and private hosts stay
// allowed for a self-hosted Bot API server.
func sendMessage(ctx context.Context, client *http.Client, baseURL, botToken string, body map[string]any) (string, error) {
	if _, err := netguard.ValidateURL(baseURL, netguard.AllowPrivate); err != nil {
		return "", &appout.PermanentError{Err: fmt.Errorf("telegram: base_url: %w", err)}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("telegram: marshal request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/bot%s/sendMessage", baseURL, botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("telegram: build request: %w", netguard.RedactError(err, botToken))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", &appout.AmbiguousError{Err: fmt.Errorf("telegram: request failed: %w", netguard.RedactError(err, botToken))}
	}
	defer resp.Body.Close()

	data, err := netguard.ReadBody(resp.Body, responseLimitBytes)
	if errors.Is(err, netguard.ErrBodyTooLarge) {
		return "", fmt.Errorf("telegram: %w", err)
	}
	if err != nil {
		return "", &appout.AmbiguousError{Err: fmt.Errorf("telegram: read response: %w", err)}
	}

	var parsed apiResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", &appout.AmbiguousError{Err: fmt.Errorf("telegram: parse response (http %d): %w", resp.StatusCode, err)}
	}
	if !parsed.OK {
		return "", classifyError(resp.StatusCode, parsed)
	}

	var result sendMessageResult
	if err := json.Unmarshal(parsed.Result, &result); err != nil {
		return "", &appout.AmbiguousError{Err: fmt.Errorf("telegram: parse result: %w", err)}
	}
	return strconv.FormatInt(result.MessageID, 10), nil
}
