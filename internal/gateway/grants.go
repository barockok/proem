package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Grant statuses.
const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusDenied   = "denied"
	statusConsumed = "consumed"
)

const (
	deviceKeyPrefix = "proem:gw:dc:"
	userKeyPrefix   = "proem:gw:uc:"

	// userCodeAlphabet has no vowels and no look-alike characters, so a
	// code cannot spell a word and is easy to type.
	userCodeAlphabet = "BCDFGHJKMNPQRSTVWXYZ"
	userCodeLen      = 8
)

var (
	errGrantNotFound = errors.New("grant not found")
	errGrantState    = errors.New("grant is not in the expected state")
)

// grant is one device authorization in progress. It lives in Redis for at
// most the device grant TTL.
type grant struct {
	Status       string `json:"status"`
	UserCode     string `json:"user_code"`
	PKCEVerifier string `json:"pkce_verifier"`
	State        string `json:"state"`
	Nonce        string `json:"nonce"`
	Email        string `json:"email"`
	Sub          string `json:"sub"`
	EncRefresh   string `json:"enc_refresh"`
	LastPoll     int64  `json:"last_poll"` // Unix milliseconds
	Created      int64  `json:"created"`   // Unix seconds
	RequesterIP  string `json:"requester_ip"`
}

// grantStore keeps device grants in Redis. It owns its own client so the
// sticky/cooldown store is unchanged.
type grantStore struct {
	rdb      redis.UniversalClient
	ttl      time.Duration
	interval time.Duration
	now      func() time.Time
	userCode func() string
}

// NewRedisClient builds the gateway's Redis client from a redis:// URL.
func NewRedisClient(redisURL string) (redis.UniversalClient, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
}

func hashDeviceCode(deviceCode string) string {
	sum := sha256.Sum256([]byte(deviceCode))
	return hex.EncodeToString(sum[:])
}

// randomToken returns n random bytes as base64url. crypto/rand.Read never
// returns an error; it aborts the program if the OS cannot supply entropy.
func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return b64.EncodeToString(buf)
}

func randomUserCode() string {
	buf := make([]byte, userCodeLen)
	_, _ = rand.Read(buf)
	var b strings.Builder
	for _, v := range buf {
		// 256 is not a multiple of 20; the bias (under 1%) does not matter
		// for a code that is single-use, short-lived and rate limited.
		b.WriteByte(userCodeAlphabet[int(v)%len(userCodeAlphabet)])
	}
	return b.String()
}

// normalizeUserCode accepts a code as typed: any case, with or without the
// dash or spaces.
func normalizeUserCode(code string) string {
	code = strings.ToUpper(code)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, code)
}

// formatUserCode renders a code as XXXX-XXXX.
func formatUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// create stores a new pending grant and returns its device and user codes.
func (s *grantStore) create(ctx context.Context, requesterIP string) (deviceCode, userCode string, err error) {
	deviceCode = randomToken(32)
	dcHash := hashDeviceCode(deviceCode)
	for attempt := 0; ; attempt++ {
		userCode = s.userCode()
		ok, err := s.rdb.SetNX(ctx, userKeyPrefix+userCode, dcHash, s.ttl).Result()
		if err != nil {
			return "", "", err
		}
		if ok {
			break
		}
		if attempt >= 4 {
			return "", "", errors.New("could not allocate a unique user code")
		}
	}
	g := grant{
		Status:      statusPending,
		UserCode:    userCode,
		Created:     s.now().Unix(),
		RequesterIP: requesterIP,
	}
	b, _ := json.Marshal(g)
	if err := s.rdb.Set(ctx, deviceKeyPrefix+dcHash, b, s.ttl).Err(); err != nil {
		return "", "", err
	}
	return deviceCode, userCode, nil
}

// byHash loads a grant by its device code hash.
func (s *grantStore) byHash(ctx context.Context, dcHash string) (grant, error) {
	b, err := s.rdb.Get(ctx, deviceKeyPrefix+dcHash).Bytes()
	if errors.Is(err, redis.Nil) {
		return grant{}, errGrantNotFound
	}
	if err != nil {
		return grant{}, err
	}
	var g grant
	if err := json.Unmarshal(b, &g); err != nil {
		return grant{}, err
	}
	return g, nil
}

// byUserCode loads a grant by its (normalized) user code.
func (s *grantStore) byUserCode(ctx context.Context, userCode string) (grant, string, error) {
	if userCode == "" {
		return grant{}, "", errGrantNotFound
	}
	dcHash, err := s.rdb.Get(ctx, userKeyPrefix+userCode).Result()
	if errors.Is(err, redis.Nil) {
		return grant{}, "", errGrantNotFound
	}
	if err != nil {
		return grant{}, "", err
	}
	g, err := s.byHash(ctx, dcHash)
	return g, dcHash, err
}

// transitionScript rewrites a grant only when its current status matches
// ARGV[1] and its state matches ARGV[2]. The TTL is kept. Returns 1 on success, 0 when the precondition failed, -1 when the
// grant is gone.
var transitionScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return -1 end
local g = cjson.decode(raw)
if g.status ~= ARGV[1] then return 0 end
if g.state ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[1], ARGV[3], 'KEEPTTL')
return 1
`)

// transition replaces a grant atomically when it is in status `from` with
// state `state`.
func (s *grantStore) transition(ctx context.Context, dcHash, from, state string, next grant) error {
	b, _ := json.Marshal(next)
	n, err := transitionScript.Run(ctx, s.rdb, []string{deviceKeyPrefix + dcHash}, from, state, b).Int()
	if err != nil {
		return err
	}
	switch n {
	case 1:
		return nil
	case -1:
		return errGrantNotFound
	default:
		return errGrantState
	}
}

// startLogin records the IdP leg's secrets on a pending grant that has not
// started a login yet, and retires its user code so it cannot be used again.
func (s *grantStore) startLogin(ctx context.Context, dcHash string, g grant, state, nonce, verifier string) error {
	g.State, g.Nonce, g.PKCEVerifier = state, nonce, verifier
	if err := s.transition(ctx, dcHash, statusPending, "", g); err != nil {
		return err
	}
	return s.rdb.Del(ctx, userKeyPrefix+g.UserCode).Err()
}

// finish marks a grant approved or denied after the IdP callback. The state
// must still match, so a callback can complete a grant only once.
func (s *grantStore) finish(ctx context.Context, dcHash string, g grant, status string) error {
	state := g.State
	next := g
	next.Status = status
	next.State, next.Nonce, next.PKCEVerifier = "", "", ""
	return s.transition(ctx, dcHash, statusPending, state, next)
}

// pollScript is the token endpoint's view of a grant, run atomically:
//   - missing           -> "expired"
//   - consumed          -> "consumed"
//   - denied            -> "denied"
//   - pending, too fast -> "slow_down" (and last_poll is updated)
//   - pending           -> "pending"   (and last_poll is updated)
//   - approved          -> the grant JSON, and the grant becomes consumed
//
// Consumption happens in the same script that checks status == approved, so
// two concurrent polls cannot both receive tokens.
var pollScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 'expired' end
local g = cjson.decode(raw)
if g.status == 'consumed' then return 'consumed' end
if g.status == 'denied' then return 'denied' end
if g.status == 'approved' then
  local out = raw
  g.status = 'consumed'
  g.enc_refresh = ''
  redis.call('SET', KEYS[1], cjson.encode(g), 'KEEPTTL')
  return out
end
local now = tonumber(ARGV[1])
local last = tonumber(g.last_poll) or 0
g.last_poll = now
redis.call('SET', KEYS[1], cjson.encode(g), 'KEEPTTL')
if last > 0 and now - last < tonumber(ARGV[2]) then return 'slow_down' end
return 'pending'
`)

// poll results other than an approved grant.
const (
	pollExpired  = "expired"
	pollConsumed = "consumed"
	pollDenied   = "denied"
	pollSlowDown = "slow_down"
	pollPending  = "pending"
)

// poll checks a device code. When the grant is approved it is consumed and
// returned; otherwise the outcome is one of the poll* constants.
func (s *grantStore) poll(ctx context.Context, deviceCode string) (grant, string, error) {
	res, err := pollScript.Run(ctx, s.rdb, []string{deviceKeyPrefix + hashDeviceCode(deviceCode)},
		s.now().UnixMilli(), s.interval.Milliseconds()).Text()
	if err != nil {
		return grant{}, "", err
	}
	switch res {
	case pollExpired, pollConsumed, pollDenied, pollSlowDown, pollPending:
		return grant{}, res, nil
	}
	var g grant
	if err := json.Unmarshal([]byte(res), &g); err != nil {
		return grant{}, "", err
	}
	return g, "", nil
}
