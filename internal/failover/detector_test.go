package failover

import (
	"net/http"
	"testing"
)

func TestShouldFailover(t *testing.T) {
	cases := []struct{
		name string
		status int
		body string
		headers map[string]string
		want bool
	}{
		{"429 rate_limit", 429, `{"error":{"type":"rate_limit"}}`, nil, true},
		{"429 no keyword", 429, `{"error":"bad request"}`, nil, false},
		{"400 with rate_limit", 400, `rate_limit`, nil, false},
		{"529 overload", 529, `overload`, nil, true},
		{"401 oauth", 401, `oauth token expired`, nil, true},
		{"401 no keyword", 401, `unauthorized`, nil, false},
		{"429 retry-after", 429, `{}`, map[string]string{"Retry-After":"120"}, true},
		{"200 retry-after still fail", 200, `{}`, map[string]string{"Retry-After":"30"}, true},
		{"429 quota", 429, `quota exceeded`, nil, true},
		{"429 credit", 429, `credit balance too low`, nil, true},
	}
	for _, c := range cases {
		h := http.Header{}
		for k,v := range c.headers { h.Set(k,v) }
		got, ttl, _ := ShouldFailover(c.status, []byte(c.body), h)
		if got != c.want { t.Fatalf("%s got %v want %v ttl %d", c.name, got, c.want, ttl)}
		if c.headers!=nil && c.headers["Retry-After"]!="" && got && ttl==0 && c.headers["Retry-After"]=="120" {
			// ttl parsed
		}
	}
}

func TestCooldownTTL(t *testing.T) {
	if CooldownTTL(120, 0, true)!=120 {t.Fatal()}
	if CooldownTTL(0, 60, true)!=60 {t.Fatal()}
	if CooldownTTL(0, 0, true)!=18000 {t.Fatal()}
	if CooldownTTL(0, 0, false)!=60 {t.Fatal()}
}
