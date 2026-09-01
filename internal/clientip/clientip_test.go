package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestNoTrustedProxiesIgnoresForwardedFor(t *testing.T) {
	r, err := NewResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := r.FromRequest(request("192.0.2.10:5555", "1.2.3.4"))
	if got != "192.0.2.10" {
		t.Fatalf("forged X-Forwarded-For must be ignored, got %q", got)
	}
}

func TestUntrustedPeerCannotForgeForwardedFor(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	got := r.FromRequest(request("192.0.2.10:5555", "1.2.3.4"))
	if got != "192.0.2.10" {
		t.Fatalf("peer outside the trusted set must not be believed, got %q", got)
	}
}

func TestTrustedPeerYieldsForwardedClient(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	got := r.FromRequest(request("10.1.2.3:443", "203.0.113.9"))
	if got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestChainOfTrustedProxiesReturnsFirstUntrusted(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	// client -> edge(10.9.9.9) -> mesh(10.1.1.1) -> us
	got := r.FromRequest(request("10.1.1.1:443", "203.0.113.9, 10.9.9.9"))
	if got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestSpoofedPrefixIsNotReturnedVerbatim(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	// A caller prepends a bogus hop; the right-to-left walk still stops at the
	// first address our proxies did not add.
	got := r.FromRequest(request("10.1.1.1:443", "9.9.9.9, 203.0.113.9"))
	if got != "203.0.113.9" {
		t.Fatalf("walk must start from the right, got %q", got)
	}
}

func TestAllHopsTrustedFallsBackToPeer(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	got := r.FromRequest(request("10.1.1.1:443", "10.2.2.2, 10.3.3.3"))
	if got != "10.1.1.1" {
		t.Fatalf("got %q", got)
	}
}

func TestMultipleHeaderValues(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8"})
	got := r.FromRequest(request("10.1.1.1:443", "203.0.113.9", "10.4.4.4"))
	if got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestBareIPTrustedEntry(t *testing.T) {
	r, err := NewResolver([]string{"10.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.FromRequest(request("10.1.1.1:443", "203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("bare IPv4 entry: %q", got)
	}
	if got := r.FromRequest(request("10.1.1.2:443", "203.0.113.9")); got != "10.1.1.2" {
		t.Fatalf("non-listed peer: %q", got)
	}
}

func TestIPv6(t *testing.T) {
	r, err := NewResolver([]string{"::1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.FromRequest(request("[::1]:8080", "2001:db8::5")); got != "2001:db8::5" {
		t.Fatalf("ipv6 forwarded: %q", got)
	}
	if got := r.FromRequest(request("[2001:db8::9]:8080")); got != "2001:db8::9" {
		t.Fatalf("ipv6 peer: %q", got)
	}
}

func TestRemoteAddrWithoutPort(t *testing.T) {
	r, _ := NewResolver(nil)
	if got := r.FromRequest(request("192.0.2.10")); got != "192.0.2.10" {
		t.Fatalf("got %q", got)
	}
}

func TestEmptyAndMalformedInput(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8", "  ", ""})
	if got := r.FromRequest(request("")); got != "" {
		t.Fatalf("empty RemoteAddr: %q", got)
	}
	// junk hops are skipped rather than returned
	if got := r.FromRequest(request("10.1.1.1:443", "not-an-ip, 203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestNilResolver(t *testing.T) {
	var r *Resolver
	if got := r.FromRequest(request("192.0.2.10:1", "1.2.3.4")); got != "192.0.2.10" {
		t.Fatalf("nil resolver must fall back to the peer, got %q", got)
	}
}

func TestNewResolverRejectsGarbage(t *testing.T) {
	if _, err := NewResolver([]string{"not-a-cidr"}); err == nil {
		t.Fatal("want error for malformed entry")
	}
	if _, err := NewResolver([]string{"10.0.0.0/99"}); err == nil {
		t.Fatal("want error for impossible prefix")
	}
}
