// infra/llmclient/transport.go
package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Config points to one OpenAI-compatible chat-completions endpoint.
// Client and Resolver each hold their own Config instance — the two
// flows must be independently configurable (different base_url/model/
// temperature/timeout, potentially different providers entirely).
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	// MaxTokens caps the response length when > 0. Zero means "let the
	// provider use its own default" — Client has always
	// left this unset; Resolver sets it from
	// entity_extraction.fallback.max_tokens to keep its small JSON
	// response bounded.
	MaxTokens int
	// ReasoningEffort optionally controls OpenAI-compatible reasoning
	// models. Ollama's /v1 endpoint accepts "none" to disable reasoning.
	ReasoningEffort string
	// EnableThinking explicitly selects thinking mode for providers such as
	// oMLX that do not treat ReasoningEffort "none" as disabling it. A nil
	// value leaves the provider's default unchanged.
	EnableThinking *bool
	// ChatTemplateKwargs are provider-specific template controls. oMLX uses
	// this to pass enable_thinking and preserve_thinking to Qwen templates.
	ChatTemplateKwargs map[string]any
	// Timeout bounds each HTTP request; zero means defaultTimeout.
	Timeout time.Duration
	// Logger receives rate-limit-retry and timeout notices; optional —
	// nil is treated as a no-op logger. Never receives API keys or
	// prompt/response content, only status codes and durations.
	Logger appout.Logger
}

// WithThinkingDisabled returns c with every known thinking switch turned
// off: reasoning_effort=none for Ollama-style /v1 endpoints, plus
// enable_thinking and the Qwen chat-template kwargs that oMLX reads.
// Reasoning models otherwise spend the response budget on a chain of
// thought instead of the required JSON payload.
func (c Config) WithThinkingDisabled() Config {
	enableThinking := false
	c.ReasoningEffort = "none"
	c.EnableThinking = &enableThinking
	c.ChatTemplateKwargs = map[string]any{
		"enable_thinking":   false,
		"preserve_thinking": false,
	}
	return c
}

type transport struct {
	cfg        Config
	httpClient *http.Client
}

// defaultTimeout applies when Config.Timeout is not positive. It is long
// because local models can take minutes on a large batch.
const defaultTimeout = 5 * time.Minute

// newTransport builds the HTTP client. BaseURL is host-operator
// configuration and often a LAN or loopback model server, so private
// hosts are allowed; link-local and cloud metadata addresses are refused at
// dial time (unless an environment proxy is set: see docs/SECURITY.md) and
// redirects follow the netguard policy.
func newTransport(cfg Config) *transport {
	return &transport{cfg: cfg, httpClient: netguard.NewClient(cfg.Timeout, defaultTimeout, netguard.AllowPrivate)}
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)        {}
func (noopLogger) Info(string, ...any)         {}
func (noopLogger) Warn(string, ...any)         {}
func (noopLogger) Error(string, error, ...any) {}

func (t *transport) logger() appout.Logger {
	if t.cfg.Logger != nil {
		return t.cfg.Logger
	}
	return noopLogger{}
}

// httpStatusError distinguishes an HTTP response with a known non-2xx
// status from other failures (network errors, JSON decode errors) so
// completeWithFallback can decide whether a bounded retry is worthwhile.
type httpStatusError struct {
	StatusCode int
	RetryAfter time.Duration // 0 when the Retry-After header was absent or unparseable
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("unexpected status %d", e.StatusCode)
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// parseRetryAfter reads a Retry-After header value as whole seconds —
// the only form Groq (and every OpenAI-compatible provider observed so
// far) emits. An HTTP-date form is not supported; an unparseable or
// empty value returns 0, meaning "use the default wait."
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(v); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

const (
	defaultRateLimitRetryWait = 1 * time.Second
	maxRateLimitRetryWait     = 3 * time.Second
)

// boundedRetryWait returns min(requested, maxRateLimitRetryWait), or
// defaultRateLimitRetryWait when requested is 0.
func boundedRetryWait(requested time.Duration) time.Duration {
	wait := requested
	if wait <= 0 {
		wait = defaultRateLimitRetryWait
	}
	if wait > maxRateLimitRetryWait {
		wait = maxRateLimitRetryWait
	}
	return wait
}

// sleepFn is the retry-wait seam; tests replace it (export_test.go) to assert
// the computed wait without spending wall-clock time.
var sleepFn = sleep

// sleep waits for d, respecting ctx cancellation — returns ctx.Err() if the
// context ends first, in which case the caller should not bother retrying.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []chatMessage  `json:"messages"`
	Temperature        float64        `json:"temperature"`
	MaxTokens          int            `json:"max_tokens,omitempty"`
	ReasoningEffort    string         `json:"reasoning_effort,omitempty"`
	EnableThinking     *bool          `json:"enable_thinking,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	ResponseFormat     map[string]any `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// completion is one chat-completion result: the raw message content plus the
// metadata needed to diagnose an unusable structured response. It never
// feeds logs or errors beyond content's byte length.
type completion struct {
	Content          string
	FinishReason     string
	CompletionTokens int // 0 when the provider omits usage
	MaxTokens        int // max_tokens actually sent; 0 = provider default
}

// chatCompletion sends systemPrompt+userPrompt and returns the raw
// message content with completion metadata; maxTokens is the max_tokens
// value sent (0 omits it). It first requests response_format: json_object; if
// the server rejects that field with a non-retryable HTTP status, the
// caller may retry once via completeWithFallback without that field.
func (t *transport) chatCompletion(ctx context.Context, systemPrompt, userPrompt string, withResponseFormat bool, maxTokens int) (completion, error) {
	reqBody := chatRequest{
		Model:              t.cfg.Model,
		Temperature:        t.cfg.Temperature,
		MaxTokens:          maxTokens,
		ReasoningEffort:    t.cfg.ReasoningEffort,
		EnableThinking:     t.cfg.EnableThinking,
		ChatTemplateKwargs: t.cfg.ChatTemplateKwargs,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}
	if withResponseFormat {
		reqBody.ResponseFormat = map[string]any{"type": "json_object"}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return completion{}, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := netguard.ValidateURL(t.cfg.BaseURL, netguard.AllowPrivate); err != nil {
		return completion{}, fmt.Errorf("base_url: %w", err)
	}
	endpoint := strings.TrimRight(t.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return completion{}, fmt.Errorf("build request: %w", netguard.RedactError(err, t.cfg.APIKey))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)
	if t.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.cfg.APIKey)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return completion{}, fmt.Errorf("do request: %w", netguard.RedactError(err, t.cfg.APIKey))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return completion{}, &httpStatusError{StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	data, err := netguard.ReadBody(resp.Body, netguard.JSONBodyLimit)
	if err != nil {
		return completion{}, fmt.Errorf("read response: %w", err)
	}
	var parsed chatResponse
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&parsed); err != nil {
		return completion{}, fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return completion{}, fmt.Errorf("response had no choices")
	}
	c := parsed.Choices[0]
	return completion{
		Content:          c.Message.Content,
		FinishReason:     c.FinishReason,
		CompletionTokens: parsed.Usage.CompletionTokens,
		MaxTokens:        maxTokens,
	}, nil
}

// completeWithFallback tries chatCompletion with response_format first.
// If that fails with a 429 or 5xx, it waits a short bounded time (honoring
// Retry-After when the provider sends one, capped at
// maxRateLimitRetryWait) and retries the SAME request once — never more
// than one extra attempt, and abandoned immediately if ctx is cancelled
// first. Only a non-retryable HTTP response is eligible for the
// compatibility retry without response_format: retrying a transport
// failure or timeout would submit the same expensive request again to an
// already unhealthy local model.
func (t *transport) completeWithFallback(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	c, err := t.complete(ctx, systemPrompt, userPrompt, t.cfg.MaxTokens)
	return c.Content, err
}

// complete is completeWithFallback with an explicit max_tokens and the full
// completion metadata, so a caller can issue a larger-budget retry.
func (t *transport) complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (completion, error) {
	c, err := t.chatCompletion(ctx, systemPrompt, userPrompt, true, maxTokens)
	if err == nil {
		return c, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.logger().Info("llmclient: timeout", "error", err.Error())
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return completion{}, fmt.Errorf("chat completion: %w", err)
	}
	if isRetryableStatus(statusErr.StatusCode) {
		t.logger().Info("llmclient: rate limited or server error, retrying once",
			"status", statusErr.StatusCode, "retry_after_ms", statusErr.RetryAfter.Milliseconds())
		if waitErr := sleepFn(ctx, boundedRetryWait(statusErr.RetryAfter)); waitErr != nil {
			return completion{}, fmt.Errorf("chat completion: %w", waitErr)
		}
		retried, retryErr := t.chatCompletion(ctx, systemPrompt, userPrompt, true, maxTokens)
		if retryErr != nil {
			return completion{}, fmt.Errorf("chat completion: %w", retryErr)
		}
		return retried, nil
	}
	c, err = t.chatCompletion(ctx, systemPrompt, userPrompt, false, maxTokens)
	if err != nil {
		return completion{}, fmt.Errorf("chat completion: %w", err)
	}
	return c, nil
}

// stripCodeFence removes a ```json ... ``` (or bare ```) fence some small
// local models wrap their JSON output in despite being told not to.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
