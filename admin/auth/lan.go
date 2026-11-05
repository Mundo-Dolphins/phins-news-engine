package auth

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
)

// ClientIP returns the peer address from r.RemoteAddr only. Forwarding
// headers are never consulted. IPv4-mapped IPv6 addresses are unmapped.
func ClientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// LANOnly answers 403 unless the client IP is inside one of allowed. An
// empty list denies everything.
func LANOnly(allowed []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip, ok := ClientIP(r); ok {
			for _, p := range allowed {
				if p.Contains(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		writeError(w, http.StatusForbidden, "forbidden", "address not allowed")
	})
}

// SameOrigin refuses state-changing requests (anything but GET/HEAD) whose
// Origin, or Referer when Origin is absent, host differs from r.Host. A
// request carrying neither header is refused.
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		if u, err := url.Parse(src); src != "" && err == nil && u.Host != "" && u.Host == r.Host {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
	})
}
