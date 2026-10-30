package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPSource names where the address a request came from is read from.
//
// This is a statement of fact about the deployment rather than a preference: the
// right value is decided by whatever sits in front of the server, and a wrong one
// fails quietly in both directions. Too trusting, and every client can invent an
// address and a rate-limit bucket to itself; not trusting enough, and everyone
// shares the proxy's bucket.
type ClientIPSource string

const (
	// ClientIPRemote reads r.RemoteAddr, which the server observed itself and
	// which therefore cannot be lied about. Correct when nothing is in front, and
	// the safe default behind a proxy too, just coarse: every client shares the
	// proxy's address.
	ClientIPRemote ClientIPSource = "remote"

	// ClientIPForwarded reads the leftmost X-Forwarded-For entry.
	//
	// The proxy must be configured to replace X-Forwarded-For, not append to it:
	// the leftmost entry is the original client for a proxy that overwrites the
	// header, and is whatever the client typed for one that appends.
	ClientIPForwarded ClientIPSource = "forwarded"

	// ClientIPCloudflare reads CF-Connecting-IP, which Cloudflare sets to the
	// address it accepted the connection from.
	//
	// Correct only where Cloudflare is the sole way in: a tunnel with nothing
	// published on the host, or an origin firewalled to Cloudflare's ranges. Any
	// route that reaches this server without passing through Cloudflare (Traefik
	// passes a client-sent CF-Connecting-IP straight through) can set the header
	// to anything it likes, and this mode will believe it.
	//
	// X-Forwarded-For is deliberately not the fallback: Cloudflare appends to it,
	// so its leftmost entry is client-supplied.
	ClientIPCloudflare ClientIPSource = "cloudflare"
)

// ParseClientIPSource validates a CLIENT_IP_SOURCE setting. It lives here rather
// than in config so the values and their meanings stay next to the code that acts
// on them.
func ParseClientIPSource(s string) (ClientIPSource, error) {
	switch src := ClientIPSource(strings.TrimSpace(s)); src {
	case ClientIPRemote, ClientIPForwarded, ClientIPCloudflare:
		return src, nil
	default:
		return "", fmt.Errorf("must be one of %s, %s or %s; got %q",
			ClientIPRemote, ClientIPForwarded, ClientIPCloudflare, s)
	}
}

// ClientIP returns the address a request came from, read as source says to.
//
// Every mode falls back to r.RemoteAddr when its header is missing or empty: a
// header that is not there means the request did not arrive the way the
// deployment claims it does, and RemoteAddr cannot be forged.
func ClientIP(r *http.Request, source ClientIPSource) string {
	switch source {
	case ClientIPForwarded:
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return bareIP(ip)
			}
		}
	case ClientIPCloudflare:
		if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
			return bareIP(ip)
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port, which happens in tests and with some listeners.
		return unbracket(strings.TrimSpace(r.RemoteAddr))
	}
	return host
}

// bareIP reduces a header value to the address alone. Some load balancers write
// "ip:port" (or "[v6]:port"); left alone, that string is not an address, so the
// rate limiter would key on it and give every source port its own bucket. A value
// that is neither an address nor host:port is returned unchanged.
func bareIP(s string) string {
	if _, err := netip.ParseAddr(unbracket(s)); err == nil {
		return unbracket(s)
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return unbracket(s)
}

// unbracket strips the brackets an IPv6 address may arrive wrapped in, since
// netip.ParseAddr wants the bare form.
func unbracket(ip string) string {
	return strings.TrimSuffix(strings.TrimPrefix(ip, "["), "]")
}
