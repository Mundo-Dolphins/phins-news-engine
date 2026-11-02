package bluesky

import (
	"bytes"
	"context"
	"encoding/base64"
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

const (
	// refreshMargin is how long before the access token's expiry the session
	// is refreshed instead of used.
	refreshMargin = 2 * time.Minute
	// fallbackTokenLife applies when an access token's exp claim is unreadable.
	fallbackTokenLife = 10 * time.Minute
	// defaultRetryAfter is used for a 429 that carries no usable reset hint.
	defaultRetryAfter = 60 * time.Second
	maxRetryAfter     = time.Hour
)

// session is the account session returned by createSession/refreshSession.
// Its tokens are secrets: nothing here is ever formatted into an error or log.
type session struct {
	did       string
	access    string
	refresh   string
	accessExp time.Time
}

type sessionResponse struct {
	AccessJWT  string `json:"accessJwt"`
	RefreshJWT string `json:"refreshJwt"`
	DID        string `json:"did"`
}

// response is one raw XRPC reply.
type response struct {
	status int
	header http.Header
	body   []byte
}

// transportError marks a failure after the request may have reached the
// server: the call failed in flight or its reply could not be read.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// do sends one request and reads the whole (bounded) reply. A *transportError
// means the request may or may not have reached the server. A reply with any HTTP status is
// returned as a response, not an error; callers classify it.
func (p *Publisher) do(ctx context.Context, endpoint, contentType string, body []byte, bearer string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Host+"/xrpc/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bluesky: build %s request: %w", endpoint, netguard.RedactError(err))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", netguard.DefaultUserAgent)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, &transportError{fmt.Errorf("bluesky: %s request failed: %w", endpoint, netguard.RedactError(err, p.secrets(bearer)...))}
	}
	defer resp.Body.Close()

	data, err := netguard.ReadBody(resp.Body, p.cfg.MaxResponseBytes)
	if err != nil {
		return nil, &transportError{fmt.Errorf("bluesky: read %s response (http %d): %w", endpoint, resp.StatusCode, err)}
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func (p *Publisher) secrets(extra ...string) []string {
	out := []string{p.cfg.AppPassword}
	for _, s := range extra {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// statusError turns a non-2xx reply into the outbox error semantics: 429 is a
// RateLimitedError, any other 4xx a PermanentError (the request itself is
// wrong), and 5xx a plain temporary error.
func statusError(endpoint string, r *response, now time.Time) error {
	var body apiErrorResponse
	_ = json.Unmarshal(r.body, &body)
	msg := fmt.Sprintf("bluesky: HTTP %d for %s", r.status, endpoint)
	switch {
	case body.Message != "" && body.Error != "":
		msg += ": " + body.Message + " (" + body.Error + ")"
	case body.Message != "":
		msg += ": " + body.Message
	case body.Error != "":
		msg += ": " + body.Error
	}
	err := errors.New(msg)

	switch {
	case r.status == http.StatusTooManyRequests:
		return &appout.RateLimitedError{Err: err, RetryAfter: retryAfter(r.header, now)}
	case r.status >= 400 && r.status < 500:
		return &appout.PermanentError{Err: err}
	default:
		return err
	}
}

// retryAfter reads how long the server asks to wait: RateLimit-Reset (a Unix
// timestamp, as the PDS sends it) first, then Retry-After in seconds.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := h.Get("RateLimit-Reset"); v != "" {
		if ts, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return clampRetry(time.Unix(ts, 0).Sub(now))
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return clampRetry(time.Duration(secs) * time.Second)
		}
	}
	return defaultRetryAfter
}

func clampRetry(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return defaultRetryAfter
	case d > maxRetryAfter:
		return maxRetryAfter
	}
	return d
}

// isExpiredToken reports whether a reply says the access token is no longer
// valid, which is a definitive rejection (nothing was processed) and so safe
// to retry after renewing the session.
func isExpiredToken(r *response) bool {
	if r.status != http.StatusUnauthorized && r.status != http.StatusBadRequest {
		return false
	}
	var body apiErrorResponse
	if json.Unmarshal(r.body, &body) != nil {
		return r.status == http.StatusUnauthorized
	}
	return body.Error == "ExpiredToken" || body.Error == "InvalidToken" || (r.status == http.StatusUnauthorized && body.Error == "")
}

// authedPost sends an authenticated request, renewing the session first when
// needed and once more if the server rejects the token as expired.
func (p *Publisher) authedPost(ctx context.Context, endpoint, contentType string, body []byte) (*response, string, error) {
	for attempt := 0; ; attempt++ {
		token, did, err := p.accessToken(ctx)
		if err != nil {
			return nil, "", err
		}
		r, err := p.do(ctx, endpoint, contentType, body, token)
		if err != nil {
			return nil, "", err
		}
		if attempt == 0 && isExpiredToken(r) {
			p.dropAccess(token)
			continue
		}
		return r, did, nil
	}
}

// accessToken returns a usable access JWT and the account DID, logging in or
// refreshing as needed. It is safe for concurrent use.
func (p *Publisher) accessToken(ctx context.Context) (token, did string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.cfg.Now()
	if p.sess != nil && p.sess.access != "" && now.Add(refreshMargin).Before(p.sess.accessExp) {
		return p.sess.access, p.sess.did, nil
	}
	if p.sess != nil && p.sess.refresh != "" {
		if s, rerr := p.exchange(ctx, "com.atproto.server.refreshSession", nil, p.sess.refresh); rerr == nil {
			p.sess = s
			return s.access, s.did, nil
		} else if ctx.Err() != nil {
			return "", "", rerr
		}
		p.sess = nil // refresh failed: fall back to a fresh login
	}
	body, _ := json.Marshal(map[string]string{"identifier": p.cfg.Handle, "password": p.cfg.AppPassword})
	s, err := p.exchange(ctx, "com.atproto.server.createSession", body, "")
	if err != nil {
		return "", "", err
	}
	p.sess = s
	return s.access, s.did, nil
}

// dropAccess forgets the access token (keeping the refresh token) when the
// server rejected it, so the next accessToken call renews it.
func (p *Publisher) dropAccess(rejected string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil && p.sess.access == rejected {
		p.sess.accessExp = time.Time{}
	}
}

// exchange performs createSession or refreshSession. Failures here happened
// before anything was posted, so none is ambiguous. A rejected login is a
// temporary error, not a permanent one: wrong credentials are repaired by the
// operator and must not discard queued posts.
func (p *Publisher) exchange(ctx context.Context, endpoint string, body []byte, bearer string) (*session, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	r, err := p.do(ctx, endpoint, contentType, body, bearer)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		err := statusError(endpoint, r, p.cfg.Now())
		var perm *appout.PermanentError
		if errors.As(err, &perm) {
			return nil, fmt.Errorf("bluesky: session rejected, check the handle and app password: %w", perm.Err)
		}
		return nil, err
	}
	var sr sessionResponse
	if err := json.Unmarshal(r.body, &sr); err != nil || sr.AccessJWT == "" || sr.DID == "" {
		return nil, fmt.Errorf("bluesky: %s: unreadable session response", endpoint)
	}
	exp := jwtExpiry(sr.AccessJWT)
	if exp.IsZero() {
		exp = p.cfg.Now().Add(fallbackTokenLife)
	}
	return &session{did: sr.DID, access: sr.AccessJWT, refresh: sr.RefreshJWT, accessExp: exp}, nil
}

// jwtExpiry reads the exp claim of a JWT without verifying it (the server
// issued it; only the expiry is needed). A zero time means unreadable.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}
