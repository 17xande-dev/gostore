package middleware

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the address a request came from.
//
// Without a proxy that is r.RemoteAddr, which the server observed itself and
// which therefore cannot be lied about. Behind one, RemoteAddr is the proxy, and
// the real client is in X-Forwarded-For — hence trustProxy, which must be off
// unless something in front of the server actually sets that header.
//
// **The proxy must be configured to replace X-Forwarded-For, not append to it.**
// This takes the leftmost entry, which is the original client for every proxy
// that overwrites the header — and is whatever the client typed for one that
// appends (nginx's $proxy_add_x_forwarded_for does append). The alternative,
// reading the rightmost entry, is safe against that but wrong on every platform
// that puts a load balancer of its own in the chain, which is most managed ones.
//
// Where this matters most is the payment callback's source-IP check, and there it
// is worth knowing that the check is defence in depth: a forged notification also
// has to carry a valid signature and be confirmed by the gateway's own servers,
// and spoofing an IP does neither.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return unbracket(ip)
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port, which happens in tests and with some listeners.
		return unbracket(strings.TrimSpace(r.RemoteAddr))
	}
	return host
}

// unbracket strips the brackets an IPv6 address may arrive wrapped in, since
// netip.ParseAddr wants the bare form.
func unbracket(ip string) string {
	return strings.TrimSuffix(strings.TrimPrefix(ip, "["), "]")
}
