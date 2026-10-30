package middleware_test

import (
	"net/http"
	"testing"

	"github.com/17xande-dev/budget/internal/middleware"
)

func TestClientIP(t *testing.T) {
	newReq := func(remoteAddr, cf, xff string) *http.Request {
		r := &http.Request{RemoteAddr: remoteAddr, Header: http.Header{}}
		if cf != "" {
			r.Header.Set("CF-Connecting-IP", cf)
		}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	for name, tc := range map[string]struct {
		source     middleware.ClientIPSource
		cf, xff    string
		remoteAddr string
		want       string
	}{
		// The default: a spoofable header is ignored, which is what stops a
		// client choosing its own rate-limit bucket behind Traefik.
		"remote ignores both headers":        {middleware.ClientIPRemote, "1.2.3.4", "5.6.7.8", "203.0.113.5:1234", "203.0.113.5"},
		"empty source behaves as remote":     {"", "1.2.3.4", "5.6.7.8", "203.0.113.5:1234", "203.0.113.5"},
		"cloudflare reads CF-Connecting-IP":  {middleware.ClientIPCloudflare, "1.2.3.4", "5.6.7.8", "10.0.0.1:1234", "1.2.3.4"},
		"cloudflare never falls back to XFF": {middleware.ClientIPCloudflare, "", "5.6.7.8", "10.0.0.1:1234", "10.0.0.1"},
		"forwarded reads leftmost XFF":       {middleware.ClientIPForwarded, "9.9.9.9", "1.2.3.4, 10.0.0.2", "10.0.0.1:1234", "1.2.3.4"},
		"forwarded ignores CF":               {middleware.ClientIPForwarded, "9.9.9.9", "", "10.0.0.1:1234", "10.0.0.1"},
		"cloudflare strips a port":           {middleware.ClientIPCloudflare, "1.2.3.4:5555", "", "10.0.0.1:1", "1.2.3.4"},
		"cloudflare strips v6 brackets+port": {middleware.ClientIPCloudflare, "[2001:db8::1]:5555", "", "10.0.0.1:1", "2001:db8::1"},
		"forwarded strips a port":            {middleware.ClientIPForwarded, "", "1.2.3.4:5555, 10.0.0.2", "10.0.0.1:1", "1.2.3.4"},
		"bare v6 is kept":                    {middleware.ClientIPCloudflare, "2001:db8::1", "", "10.0.0.1:1", "2001:db8::1"},
		"v6 peer":                            {middleware.ClientIPRemote, "", "", "[2001:db8::7]:99", "2001:db8::7"},
		"peer without a port":                {middleware.ClientIPRemote, "", "", "203.0.113.5", "203.0.113.5"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := middleware.ClientIP(newReq(tc.remoteAddr, tc.cf, tc.xff), tc.source); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseClientIPSource(t *testing.T) {
	for _, ok := range []string{"remote", "forwarded", " cloudflare "} {
		if _, err := middleware.ParseClientIPSource(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := middleware.ParseClientIPSource("trust-everyone"); err == nil {
		t.Error("an unknown source was accepted")
	}
}
