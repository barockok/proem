// Package clientip resolves the address of the caller, accounting for proxies
// in front of the server.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// Resolver determines the caller's IP. X-Forwarded-For is only consulted when
// the immediate peer is a trusted proxy: the header is caller-supplied and
// trivially forged, so honouring it from an untrusted peer would let any
// client claim any address.
type Resolver struct {
	trusted []*net.IPNet
}

// NewResolver builds a resolver from CIDR blocks (or bare IPs) naming the
// proxies in front of this server. With no entries, X-Forwarded-For is ignored
// entirely and the peer address is always used.
func NewResolver(cidrs []string) (*Resolver, error) {
	r := &Resolver{}
	for _, raw := range cidrs {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			// a bare address is a /32 or /128
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, &net.ParseError{Type: "trusted proxy address", Text: entry}
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			entry = entry + "/" + itoa(bits)
		}
		_, block, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, err
		}
		r.trusted = append(r.trusted, block)
	}
	return r, nil
}

// FromRequest returns the caller's IP address.
func (r *Resolver) FromRequest(req *http.Request) string {
	peer := hostOnly(req.RemoteAddr)
	if r == nil || len(r.trusted) == 0 || !r.isTrusted(peer) {
		return peer
	}
	// The peer is a known proxy, so walk X-Forwarded-For from the right and
	// return the first address that is not itself one of our proxies.
	for _, hop := range reverse(forwardedFor(req)) {
		if !r.isTrusted(hop) {
			return hop
		}
	}
	return peer
}

func (r *Resolver) isTrusted(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, block := range r.trusted {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardedFor returns the X-Forwarded-For hops in order, left to right.
func forwardedFor(req *http.Request) []string {
	var hops []string
	for _, header := range req.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			if hop := hostOnly(strings.TrimSpace(part)); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	return hops
}

// hostOnly strips any port and brackets, leaving a bare IP.
func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
