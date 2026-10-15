// Package netguard holds the hardening shared by every outbound HTTP
// client in this library: bounded body reads, a redirect policy, URL and
// destination-address validation (SSRF guard, checked again at dial time so
// DNS rebinding cannot bypass it), and redaction of credentials from
// errors. See docs/SECURITY.md, "Network client hardening".
package netguard

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	// FeedBodyLimit is the default cap for feed and HTML-like bodies.
	FeedBodyLimit int64 = 10 << 20
	// JSONBodyLimit is the default cap for JSON API bodies.
	JSONBodyLimit int64 = 1 << 20
	// MaxRedirects is the number of redirects a client follows.
	MaxRedirects = 5
	// DefaultUserAgent is the neutral User-Agent sent by clients whose
	// caller configures none.
	DefaultUserAgent = "phins-news-engine/1.0"
)

var (
	// ErrBodyTooLarge reports a response body above its configured cap.
	ErrBodyTooLarge = errors.New("response body too large")
	// ErrDisallowedURL reports a URL or resolved address rejected by policy.
	ErrDisallowedURL = errors.New("destination not allowed")
)

// bodyTooLargeError keeps the historical "response exceeds N bytes" text
// while matching ErrBodyTooLarge.
type bodyTooLargeError struct{ limit int64 }

func (e *bodyTooLargeError) Error() string {
	return fmt.Sprintf("response exceeds %d bytes", e.limit)
}

func (e *bodyTooLargeError) Is(target error) bool { return target == ErrBodyTooLarge }

// BodyTooLarge returns the error ReadBody reports for a body above limit:
// "response exceeds <limit> bytes", matching ErrBodyTooLarge. Callers that
// enforce a cap on bytes they already hold use it to report the same
// error.
func BodyTooLarge(limit int64) error { return &bodyTooLargeError{limit: limit} }

// ReadBody reads at most limit bytes from r. A body longer than limit is
// an error matching ErrBodyTooLarge, never silently truncated. A
// non-positive limit means JSONBodyLimit.
func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = JSONBodyLimit
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &bodyTooLargeError{limit: limit}
	}
	return data, nil
}

// TimeoutOr returns d, or def when d is not positive: no client in this
// library runs without a timeout.
func TimeoutOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// CheckRedirect is an http.Client.CheckRedirect that follows at most
// MaxRedirects redirects (via holds every earlier request, so the n-th
// redirect sees len(via) == n) and refuses a downgrade from https to http.
// net/http already drops Authorization and Cookie headers when a redirect
// changes host.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("refusing redirect from https to http")
	}
	return nil
}

// HostPolicy selects which destination addresses a client may reach.
type HostPolicy int

const (
	// BlockPrivate rejects loopback, private (RFC 1918, RFC 4193), CGNAT,
	// link-local, unspecified and multicast destinations. It is the
	// default for URLs that come from a source catalog (feeds).
	BlockPrivate HostPolicy = iota
	// AllowPrivate allows loopback and private networks — for services the
	// host operator runs (an RSS bridge, an LLM server, a SearXNG
	// instance) — but still rejects link-local (including the
	// 169.254.169.254 metadata endpoint), unspecified and multicast
	// destinations, and the cloud metadata addresses that sit inside
	// private ranges (see metadataAddrs).
	AllowPrivate
)

func (p HostPolicy) String() string {
	if p == AllowPrivate {
		return "allow-private"
	}
	return "block-private"
}

// PolicyFor maps an "allow private hosts" option to a HostPolicy.
func PolicyFor(allowPrivateHosts bool) HostPolicy {
	if allowPrivateHosts {
		return AllowPrivate
	}
	return BlockPrivate
}

var (
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	broadcast   = netip.MustParseAddr("255.255.255.255")
	// metadataAddrs are cloud metadata endpoints outside link-local space,
	// refused under every policy: AWS's IPv6 endpoint (inside the RFC 4193
	// range) and Alibaba Cloud's (inside CGNAT). AllowPrivate would
	// otherwise permit them.
	metadataAddrs = []netip.Prefix{
		netip.MustParsePrefix("fd00:ec2::254/128"),
		netip.MustParsePrefix("100.100.100.200/32"),
	}
)

// isMetadataAddr reports whether a is a known cloud metadata endpoint.
func isMetadataAddr(a netip.Addr) bool {
	for _, p := range metadataAddrs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// addrAllowed reports whether policy permits connecting to a.
func addrAllowed(a netip.Addr, policy HostPolicy) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || thisNetwork.Contains(a) || a == broadcast ||
		isMetadataAddr(a) {
		return false
	}
	if policy == AllowPrivate {
		return true
	}
	return !a.IsLoopback() && !a.IsPrivate() && !cgnat.Contains(a)
}

// ValidateURL parses raw and enforces: http or https scheme, a host, no
// userinfo, and — for an IP-literal or "localhost" host — the address
// policy. Hostnames are checked again after resolution by the transport
// from NewTransport. Errors match ErrDisallowedURL and never contain the
// query string or credentials.
func ValidateURL(raw string, policy HostPolicy) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot parse %s", ErrDisallowedURL, RedactURL(raw))
	}
	safe := RedactURL(raw)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme must be http or https: %s", ErrDisallowedURL, safe)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL are not allowed: %s", ErrDisallowedURL, safe)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, fmt.Errorf("%w: missing host: %s", ErrDisallowedURL, safe)
	}
	if policy == BlockPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
		return nil, fmt.Errorf("%w: host %s is private (%s)", ErrDisallowedURL, host, policy)
	}
	if a, err := netip.ParseAddr(host); err == nil && !addrAllowed(a, policy) {
		return nil, fmt.Errorf("%w: address %s is not reachable under %s", ErrDisallowedURL, host, policy)
	}
	return u, nil
}

// dialControl rejects a resolved destination address that policy forbids.
// Running in net.Dialer.Control it sees the final IP of every connection
// attempt, so a hostname that resolves (or re-resolves) to a forbidden
// address is refused.
func dialControl(policy HostPolicy) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparseable dial address", ErrDisallowedURL)
		}
		if !addrAllowed(ap.Addr(), policy) {
			return fmt.Errorf("%w: address %s is not reachable under %s", ErrDisallowedURL, ap.Addr(), policy)
		}
		return nil
	}
}

// NewTransport returns a clone of http.DefaultTransport whose dialer
// enforces policy on every resolved address. Under BlockPrivate the
// environment proxy is disabled: a proxy would dial the target on the
// client's behalf and bypass the address check.
func NewTransport(policy HostPolicy) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, MaxIdleConns: 100,
			IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	}
	tr := base.Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: dialControl(policy)}
	tr.DialContext = dialer.DialContext
	if policy == BlockPrivate {
		tr.Proxy = nil
	}
	return tr
}

// NewClient returns an http.Client with TimeoutOr(timeout, def), the
// redirect policy and a NewTransport(policy) transport.
func NewClient(timeout, def time.Duration, policy HostPolicy) *http.Client {
	return &http.Client{
		Timeout:       TimeoutOr(timeout, def),
		Transport:     NewTransport(policy),
		CheckRedirect: CheckRedirect,
	}
}

const redacted = "REDACTED"

var (
	botTokenPath  = regexp.MustCompile(`/bot[0-9]+:[^/?#\s"]*`)
	queryValue    = regexp.MustCompile(`([?&][^=&?#\s"]+=)[^&#\s"]*`)
	bearerValue   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	userinfoInURL = regexp.MustCompile(`//[^/@\s"]+@`)
)

// redactText applies the generic credential patterns to free text.
func redactText(s string) string {
	s = botTokenPath.ReplaceAllString(s, "/bot["+redacted+"]")
	s = queryValue.ReplaceAllString(s, "${1}"+redacted)
	s = bearerValue.ReplaceAllString(s, "$1 ["+redacted+"]")
	s = userinfoInURL.ReplaceAllString(s, "//")
	return s
}

// RedactURL returns raw without userinfo, fragment or query values (keys
// are kept for diagnostics) and with a Telegram-style /bot<token>/ path
// segment masked. An unparseable URL is cut at its query.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			raw = raw[:i] + "?" + redacted
		}
		return redactText(raw)
	}
	var b strings.Builder
	if u.Scheme != "" {
		b.WriteString(u.Scheme + ":")
	}
	if u.Host != "" {
		b.WriteString("//" + u.Host)
	}
	b.WriteString(botTokenPath.ReplaceAllString(u.EscapedPath(), "/bot["+redacted+"]"))
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			key, _, _ := strings.Cut(p, "=")
			parts[i] = key + "=" + redacted
		}
		b.WriteString("?" + strings.Join(parts, "&"))
	}
	return b.String()
}

// redactedError carries a scrubbed message and the original cause.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// redactedURLError replaces a *url.Error: it unwraps to the transport
// error (so errors.Is(err, context.DeadlineExceeded) and net.Error checks
// keep working) but no longer exposes the *url.Error and its URL.
type redactedURLError struct {
	redactedError
	urlErr *url.Error
}

func (e *redactedURLError) Timeout() bool { return e.urlErr.Timeout() }

// Temporary mirrors (*url.Error).Temporary so the value stays a net.Error.
func (e *redactedURLError) Temporary() bool { return e.urlErr.Temporary() }

// RedactError scrubs credentials from err's message: the URL of any
// *url.Error in the chain (query values, userinfo, /bot<token>/), bearer
// or basic credentials, and every non-blank value in secrets. Call it
// where the error is first received (right after http.Client.Do or
// http.NewRequest): when the chain holds a *url.Error, the result unwraps
// to that error's cause, not to the *url.Error itself. An error with
// nothing to scrub is returned unchanged.
func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	out := msg
	var ue *url.Error
	hasURLErr := errors.As(err, &ue)
	if hasURLErr && ue.URL != "" {
		out = strings.ReplaceAll(out, ue.URL, RedactURL(ue.URL))
	}
	out = redactText(out)
	for _, s := range secrets {
		if s = strings.TrimSpace(s); s != "" {
			out = strings.ReplaceAll(out, s, "["+redacted+"]")
		}
	}
	switch {
	case hasURLErr:
		return &redactedURLError{redactedError: redactedError{msg: out, cause: ue.Err}, urlErr: ue}
	case out == msg:
		return err
	default:
		return &redactedError{msg: out, cause: err}
	}
}
