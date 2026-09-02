package failover

import (
	"net/http"
	"strconv"
	"strings"
)

var keywords = []string{"rate_limit", "overload", "oauth", "quota", "credit", "rate limit", "insufficient"}

// MayFailover reports whether a response could still trigger failover, judged
// from its status and headers alone. The proxy calls this before reading any
// body: a response that cannot fail over is streamed straight through to the
// client, while a candidate is buffered so ShouldFailover can inspect it.
//
// It must stay in agreement with ShouldFailover, which is the authority on the
// final decision.
func MayFailover(status int, headers http.Header) bool {
	if headers.Get("Retry-After") != "" {
		return true
	}
	return status == 429 || status == 401 || status == 529
}

// ShouldFailover returns (shouldFailover, ttlSec, reason).
func ShouldFailover(status int, body []byte, headers http.Header) (bool, int, string) {
	// retry-after header alone triggers failover regardless of body
	if ra := headers.Get("Retry-After"); ra != "" {
		if ttl, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && ttl > 0 {
			return true, ttl, "retry-after"
		}
		// non-numeric retry-after still signals rate limit
		return true, 0, "retry-after"
	}
	if status != 429 && status != 401 && status != 529 {
		return false, 0, ""
	}
	lower := strings.ToLower(string(body))
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true, 0, kw
		}
	}
	// 429/529 without keyword still considered failover if body non-empty error-like?
	// To avoid false positives, require keyword. 401 with oauth keyword only.
	// So status match but no keyword -> no failover (let caller retry only if explicit)
	return false, 0, ""
}

// CooldownTTL resolves ttl from detector + member default.
func CooldownTTL(detectedTTL int, memberCooldown int, isAnthropic bool) int {
	if detectedTTL > 0 {
		return detectedTTL
	}
	if memberCooldown > 0 {
		return memberCooldown
	}
	if isAnthropic {
		return 18000
	}
	return 60
}
