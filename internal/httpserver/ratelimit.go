package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
)

// Rate limits on the two unauthenticated routes.
//
// Both spend a full Argon2id verification per request, and login spends one
// whether or not the address has an account — deliberately, so that a failed
// attempt costs the same either way and does not answer "is this a user?" by
// how long it took. That is what makes the cost impossible to avoid, so it has
// to be bounded from outside instead.
//
// The shape is a burst plus a slow refill rather than a count per window. A
// player who mistypes a password four times running is nowhere near the burst;
// a program working through a list spends it in seconds and is left with the
// refill rate, which is far below what guessing needs. Registration is rarer
// than logging in and writes a row as well, so it is allowed less.
const (
	loginBurst       = 5
	loginRefill      = 30 * time.Second
	loginEmailBurst  = 5
	loginEmailRefill = time.Minute

	registerBurst       = 5
	registerRefill      = 10 * time.Minute
	registerEmailBurst  = 3
	registerEmailRefill = 10 * time.Minute

	// maxTrackedKeys bounds one limiter's memory. A stranger sending a million
	// distinct addresses must not be able to make the server remember a million
	// of them; past this, entries are dropped rather than accumulated.
	maxTrackedKeys = 8192
)

// An attemptLimit rate-limits an unauthenticated route from both ends.
//
// Limiting only one end leaves the other open: many addresses from one host, or
// one address from many hosts. Neither limit alone catches both.
type attemptLimit struct {
	source *limiter
	email  *limiter

	// clientIP attributes a request to a source. When nil the source is the
	// direct peer; [New] sets it to a resolver that reads the forwarded chain
	// for requests arriving through a configured trusted proxy.
	clientIP func(*http.Request) string
}

// allow reports whether the attempt may proceed, and how long the caller should
// be told to wait when it may not.
//
// A refused source does not also spend the address's budget. Charging it would
// let anyone flooding one host push somebody else's address out of its own
// allowance for free.
func (a *attemptLimit) allow(r *http.Request, email string) (time.Duration, bool) {
	source := sourceKey(r)
	if a.clientIP != nil {
		source = a.clientIP(r)
	}
	if retry, ok := a.source.allow(source); !ok {
		return retry, false
	}
	return a.email.allow(emailKey(email))
}

// sourceKey is the direct peer a request came from: r.RemoteAddr's host and
// nothing else.
//
// It reads no forwarded header, because any client can write one. Attributing a
// request to something other than its peer is [trustedProxies.clientIP]'s job,
// and only for a request that actually arrived through a configured proxy.
func sourceKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// trustedProxies is the set of proxy addresses whose forwarding headers may be
// believed. It is empty unless -trusted-proxies is configured, and an empty set
// reads no forwarded header at all — the safe default.
type trustedProxies []netip.Prefix

// ParseTrustedProxies parses a comma-separated list of CIDRs into the set of
// proxies allowed to set X-Forwarded-For. A bare IP address is accepted as a
// single host. An empty string is no proxies.
//
// It is validated at startup so a malformed list stops the server rather than
// silently disabling the feature it was meant to enable.
func ParseTrustedProxies(list string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(field); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not a CIDR or IP address", field)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// contains reports whether an address string falls in the trusted set.
func (t trustedProxies) contains(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range t {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP is the address a request is attributed to for rate limiting.
//
// With no trusted proxies configured, or a request that did not arrive through
// one, it is the direct peer and the forwarded chain is ignored: X-Forwarded-For
// is attacker-controlled, so believing it from an untrusted peer would let a
// client mint a fresh bucket per request and turn the source limit off.
//
// Only when the peer is a configured proxy is the chain read, and then from the
// right — the end nearest us, appended by the closest proxy — taking the first
// address that is not itself a trusted hop. Anything further left the client
// could have forged, so the walk stops at the edge of the trust rather than
// running on to the leftmost entry. An absent or entirely-trusted chain falls
// back to the peer.
func (t trustedProxies) clientIP(r *http.Request) string {
	peer := sourceKey(r)
	if len(t) == 0 || !t.contains(peer) {
		return peer
	}
	hops := forwardedFor(r)
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" || t.contains(hop) {
			continue
		}
		// A trusted proxy set this, so it is the best attribution available.
		// Normalize a parseable address so the same client keys the same way
		// whether it arrives mapped into IPv6 or not; keep anything else as-is.
		if addr, err := netip.ParseAddr(hop); err == nil {
			return addr.Unmap().String()
		}
		return hop
	}
	return peer
}

// forwardedFor is the X-Forwarded-For chain left to right. net/http can present
// the header as several values and each value may itself be comma-separated, so
// both are flattened into one list.
func forwardedFor(r *http.Request) []string {
	var hops []string
	for _, value := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(value, ",")...)
	}
	return hops
}

// emailKey is the bucket a submitted address falls in.
//
// It is the normalized address, so Player@Example.COM and player@example.com
// are one bucket rather than two ways to spend two. An address that will not
// normalize still gets a bucket, because a login carrying one still costs a
// decoy verification.
func emailKey(email string) string {
	if address, err := auth.NormalizeEmail(email); err == nil {
		return address
	}
	trimmed := strings.ToLower(strings.TrimSpace(email))
	if len(trimmed) > auth.MaxEmailLength {
		trimmed = trimmed[:auth.MaxEmailLength]
	}
	return trimmed
}

// A limiter is a token bucket per key: burst tokens to spend, one earned back
// every refill.
//
// It is safe for concurrent use, and it is bounded — see prune. Nothing here
// needs more than the standard library, and a limiter that only has to answer
// "may this request run?" is smaller than the dependency that would answer it.
type limiter struct {
	burst  float64
	refill time.Duration
	max    int

	// now is the clock, so tests can move time rather than wait for it.
	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]bucket
	lastSweep time.Time
}

// A bucket is one key's allowance: the tokens it had when it was last seen, and
// when that was. Tokens earned since are worked out on the next look rather
// than by anything ticking.
type bucket struct {
	tokens float64
	seen   time.Time
}

func newLimiter(burst int, refill time.Duration, max int) *limiter {
	if max < 1 {
		// One key is the smallest map that can hold the request being served.
		max = 1
	}
	return &limiter{
		burst:   float64(burst),
		refill:  refill,
		max:     max,
		now:     time.Now,
		buckets: make(map[string]bucket),
	}
}

// allow spends a token for key.
//
// It reports whether there was one to spend, and if there was not, how long
// until there is.
func (l *limiter) allow(key string) (time.Duration, bool) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, tracked := l.buckets[key]
	if tracked {
		b.tokens = l.refilled(b, now)
	} else {
		// Room is made before the key is added, so the map never exceeds its
		// cap and the arriving key is never the one dropped to make space.
		l.prune(now)
		b.tokens = l.burst
	}
	b.seen = now

	if b.tokens < 1 {
		l.buckets[key] = b
		return time.Duration((1 - b.tokens) * float64(l.refill)), false
	}

	b.tokens--
	l.buckets[key] = b
	return 0, true
}

// refilled is the tokens a bucket has now.
func (l *limiter) refilled(b bucket, now time.Time) float64 {
	elapsed := now.Sub(b.seen)
	if elapsed <= 0 {
		return b.tokens
	}
	return min(l.burst, b.tokens+float64(elapsed)/float64(l.refill))
}

// prune makes room for one more key.
//
// Entries are pruned here, on the way past, rather than by the reaper in
// cmd/zorkd: a limiter that prunes on access owns no goroutine and no shutdown,
// and there is nothing durable behind it that would still be there if the
// process were not. A bucket back at its full burst is indistinguishable from
// one that was never recorded, so dropping it changes no later decision, and
// sweeping those keeps the map to roughly the keys that have been active.
//
// The cap is the part that has to hold against a stranger sending endless
// distinct keys faster than they refill. When the full ones are not enough, the
// fullest of what is left goes: it is the entry closest to being forgotten
// anyway, so it returns the least to whoever is filling the map.
func (l *limiter) prune(now time.Time) {
	// A sweep runs when the map is at its cap, and otherwise no oftener than the
	// time an empty bucket takes to fill, which is the soonest anything recorded
	// could have become droppable.
	if len(l.buckets) < l.max && now.Sub(l.lastSweep) < time.Duration(l.burst)*l.refill {
		return
	}
	l.lastSweep = now

	for key, b := range l.buckets {
		if l.refilled(b, now) >= l.burst {
			delete(l.buckets, key)
		}
	}

	for len(l.buckets) >= l.max {
		fullest, most := "", -1.0
		for key, b := range l.buckets {
			if tokens := l.refilled(b, now); tokens > most {
				fullest, most = key, tokens
			}
		}
		delete(l.buckets, fullest)
	}
}
