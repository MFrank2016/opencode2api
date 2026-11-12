package gateway

import (
	"net"
	"net/http"
	"regexp"
	"strings"

	"opencode2api/internal/identity"
)

// uaVersionPattern collapses version-like tokens so a CLI upgrade does not
// fragment one client's identity bucket ("opencode/2.0.32" -> "opencode/v").
var uaVersionPattern = regexp.MustCompile(`v?[0-9][\w.+-]*`)

// deriveRequestIDs applies the optional project pool while deriving identity.
// The pool only changes the project: keeping the session stable would pin one
// conversation to a single exit for the whole TTL, which is the opposite of
// what the exit rotation needs.
func (g *Gateway) deriveRequestIDs(r *http.Request, body map[string]any, scope string) identity.RequestIDs {
	var pool *identity.Pool
	if g != nil {
		pool = g.identityPool
	}
	if pool == nil {
		return identity.DeriveRequestIDs(r, body, scope)
	}
	return identity.DeriveRequestIDsWithPool(r, body, scope, pool, g.clientBucketKey(r))
}

// clientBucketKey fingerprints the client for the project pool.
func (g *Gateway) clientBucketKey(r *http.Request) string {
	if g == nil {
		return ""
	}
	return clientIP(r, g.cfg.Identity.TrustForwarded) + "|" + userAgentFamily(r.UserAgent())
}

// clientIP returns the caller address, optionally trusting the first
// X-Forwarded-For entry. Forwarded headers are only meaningful when a trusted
// proxy overwrites them, which is why the operator opts in.
func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// userAgentFamily reduces a User-Agent to a stable family: versions change with
// every release and must not fragment an identity, but two different agents on
// one machine still land in different buckets.
func userAgentFamily(userAgent string) string {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return "anon"
	}
	ua = uaVersionPattern.ReplaceAllString(ua, "v")
	if len(ua) > 80 {
		ua = ua[:80]
	}
	return ua
}
