package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A clock a test moves by hand. Nothing here sleeps: a limiter that had to be
// waited out would make its tests slow and its failures intermittent.
type clock struct {
	mu   sync.Mutex
	when time.Time
}

func newClock() *clock {
	return &clock{when: time.Date(1977, time.June, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.when
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.when = c.when.Add(d)
}

// testLimiter is a limiter on a clock the test owns.
func testLimiter(burst int, refill time.Duration, max int) (*limiter, *clock) {
	c := newClock()
	l := newLimiter(burst, refill, max)
	l.now = c.now
	return l, c
}

// tracked is how many keys the limiter is remembering.
func (l *limiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func TestLimiterAllowsTheBurstAndRefusesPastIt(t *testing.T) {
	l, _ := testLimiter(3, time.Minute, 16)

	for i := 1; i <= 3; i++ {
		if _, ok := l.allow("key"); !ok {
			t.Fatalf("attempt %d was refused, want the burst of 3 allowed", i)
		}
	}

	retry, ok := l.allow("key")
	if ok {
		t.Fatal("the fourth attempt was allowed, want it refused")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("retry = %v, want a wait inside one refill", retry)
	}
}

func TestLimiterRecoversWithTime(t *testing.T) {
	l, c := testLimiter(3, time.Minute, 16)

	for i := 0; i < 3; i++ {
		l.allow("key")
	}
	if _, ok := l.allow("key"); ok {
		t.Fatal("the burst was not spent")
	}

	// One refill buys one attempt, and only one.
	c.advance(time.Minute)
	if _, ok := l.allow("key"); !ok {
		t.Error("nothing was allowed after one refill")
	}
	if _, ok := l.allow("key"); ok {
		t.Error("one refill bought two attempts")
	}

	// Long enough, and the whole burst is back, and no more than the burst.
	c.advance(time.Hour)
	for i := 1; i <= 3; i++ {
		if _, ok := l.allow("key"); !ok {
			t.Fatalf("attempt %d after a long wait was refused", i)
		}
	}
	if _, ok := l.allow("key"); ok {
		t.Error("waiting longer accumulated more than the burst")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := testLimiter(2, time.Minute, 16)

	for i := 0; i < 2; i++ {
		l.allow("one")
	}
	if _, ok := l.allow("one"); ok {
		t.Fatal("the first key's burst was not spent")
	}
	if _, ok := l.allow("two"); !ok {
		t.Error("a second key was refused on the first key's account")
	}
}

// The map is bounded. A stranger sending endless distinct keys must not be able
// to make the limiter remember them all.
func TestLimiterStaysBounded(t *testing.T) {
	const max = 8
	l, _ := testLimiter(4, time.Minute, max)

	for i := 0; i < 10_000; i++ {
		l.allow("key-" + strconv.Itoa(i))
	}

	if got := l.tracked(); got > max {
		t.Errorf("tracked = %d, want no more than %d", got, max)
	}
}

// An idle key is forgotten once its bucket is full again, because a full bucket
// and no bucket at all decide every later attempt the same way.
func TestLimiterForgetsIdleKeys(t *testing.T) {
	l, c := testLimiter(2, time.Minute, 64)

	l.allow("gone")
	if got := l.tracked(); got != 1 {
		t.Fatalf("tracked = %d, want 1", got)
	}

	// Well past the two minutes "gone" needs to refill.
	c.advance(10 * time.Minute)
	l.allow("here")

	if got := l.tracked(); got != 1 {
		t.Errorf("tracked = %d, want only the key that is still active", got)
	}
	if _, ok := l.buckets["gone"]; ok {
		t.Error("an idle key was kept")
	}
}

// The limiter is asked from every request goroutine at once, and the burst is
// still the burst. Run under -race.
func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	const (
		burst   = 10
		callers = 50
		each    = 4
	)
	l, _ := testLimiter(burst, time.Hour, 64)

	var allowed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, ok := l.allow("key"); ok {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != burst {
		t.Errorf("allowed = %d, want exactly the burst of %d", got, burst)
	}
}

// Limiting only the source leaves one address open from many hosts, and
// limiting only the address leaves many addresses open from one host. Both are
// limited, and neither spends the other.
func TestAttemptLimitCatchesBothShapes(t *testing.T) {
	attempt := func(limit *attemptLimit, host, email string) bool {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = host + ":1024"
		_, ok := limit.allow(r, email)
		return ok
	}

	t.Run("many addresses from one host", func(t *testing.T) {
		limit := &attemptLimit{
			source: newLimiter(3, time.Minute, 64),
			email:  newLimiter(3, time.Minute, 64),
		}

		for i := 0; i < 3; i++ {
			if !attempt(limit, "198.51.100.1", "player"+strconv.Itoa(i)+"@example.com") {
				t.Fatalf("attempt %d was refused inside the burst", i)
			}
		}
		if attempt(limit, "198.51.100.1", "player9@example.com") {
			t.Error("a fourth address from the same host was allowed")
		}
		// Another host is unaffected: it spent nothing.
		if !attempt(limit, "198.51.100.2", "player9@example.com") {
			t.Error("a different host was refused on the first host's account")
		}
	})

	t.Run("one address from many hosts", func(t *testing.T) {
		limit := &attemptLimit{
			source: newLimiter(3, time.Minute, 64),
			email:  newLimiter(3, time.Minute, 64),
		}

		for i := 0; i < 3; i++ {
			if !attempt(limit, "198.51.100."+strconv.Itoa(i), "player@example.com") {
				t.Fatalf("attempt %d was refused inside the burst", i)
			}
		}
		if attempt(limit, "198.51.100.9", "player@example.com") {
			t.Error("a fourth host reaching the same address was allowed")
		}
		// The address is spent; another address from that host is not.
		if !attempt(limit, "198.51.100.9", "other@example.com") {
			t.Error("a different address was refused on the first address's account")
		}
	})

	// A host that has run out does not also spend the address it named, or
	// anyone flooding one host could push somebody else's address out of its own
	// allowance for nothing.
	t.Run("a refused source spends no address", func(t *testing.T) {
		limit := &attemptLimit{
			source: newLimiter(1, time.Minute, 64),
			email:  newLimiter(2, time.Minute, 64),
		}

		attempt(limit, "198.51.100.1", "player@example.com")
		for i := 0; i < 5; i++ {
			attempt(limit, "198.51.100.1", "player@example.com")
		}

		// One of the address's two tokens was spent by the attempt that got
		// through; the refused ones spent nothing.
		if !attempt(limit, "198.51.100.2", "player@example.com") {
			t.Error("the address was spent by attempts the source limit refused")
		}
	})
}

// Case and surrounding space are not two addresses, and an address that will
// not parse is still keyed rather than pooled with every other bad one.
func TestEmailKeyNormalizes(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{"lowercased and trimmed", "  Player@Example.COM ", "player@example.com"},
		{"already normal", "player@example.com", "player@example.com"},
		{"not an address", " Not An Address ", "not an address"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emailKey(tt.email); got != tt.want {
				t.Errorf("emailKey(%q) = %q, want %q", tt.email, got, tt.want)
			}
		})
	}

	long := strings.Repeat("x", 4096)
	if got := emailKey(long); len(got) > 254 {
		t.Errorf("emailKey() kept %d bytes of an oversized address", len(got))
	}
}

func TestSourceKeyIsTheHost(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"198.51.100.7:54321", "198.51.100.7"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"no-port-here", "no-port-here"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/login", nil)
			r.RemoteAddr = tt.addr
			if got := sourceKey(r); got != tt.want {
				t.Errorf("sourceKey(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// mustProxies parses a trusted-proxy list a test controls.
func mustProxies(t *testing.T, list string) trustedProxies {
	t.Helper()
	prefixes, err := ParseTrustedProxies(list)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(%q) error = %v", list, err)
	}
	return trustedProxies(prefixes)
}

// requestFrom builds a request from a peer, with an optional X-Forwarded-For
// chain given as one or more header values.
func requestFrom(peer string, forwarded ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = peer
	for _, value := range forwarded {
		r.Header.Add("X-Forwarded-For", value)
	}
	return r
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("CIDRs, bare IPs, and blanks", func(t *testing.T) {
		got, err := ParseTrustedProxies(" 127.0.0.1 , 10.0.0.0/8 ,, ::1 ")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("parsed %d prefixes, want 3: %v", len(got), got)
		}
	})

	t.Run("empty is no proxies", func(t *testing.T) {
		got, err := ParseTrustedProxies("")
		if err != nil || len(got) != 0 {
			t.Fatalf("ParseTrustedProxies(\"\") = %v, %v; want empty, nil", got, err)
		}
	})

	t.Run("a malformed entry is refused", func(t *testing.T) {
		if _, err := ParseTrustedProxies("127.0.0.1, not-an-ip"); err == nil {
			t.Fatal("ParseTrustedProxies() accepted a malformed entry, want an error")
		}
	})
}

// clientIP reads X-Forwarded-For only for a request whose direct peer is a
// trusted proxy, and then only as far as the trust reaches.
func TestClientIPPeelsThroughTrustedProxiesOnly(t *testing.T) {
	tests := []struct {
		name      string
		trusted   string
		peer      string
		forwarded []string
		want      string
	}{
		{"no proxies configured ignores the header", "", "127.0.0.1:5000", []string{"203.0.113.7"}, "127.0.0.1"},
		{"an untrusted peer ignores the header", "127.0.0.1/32", "203.0.113.9:5000", []string{"203.0.113.7"}, "203.0.113.9"},
		{"a trusted peer, one hop", "127.0.0.1/32", "127.0.0.1:5000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"a forged left prefix is never reached", "127.0.0.1/32", "127.0.0.1:5000", []string{"1.2.3.4, 203.0.113.7"}, "203.0.113.7"},
		{"the walk peels every trusted hop", "10.0.0.0/8", "10.0.0.2:5000", []string{"203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"an absent chain falls back to the peer", "127.0.0.1/32", "127.0.0.1:5000", nil, "127.0.0.1"},
		{"an all-trusted chain falls back to the peer", "10.0.0.0/8", "10.0.0.2:5000", []string{"10.0.0.9"}, "10.0.0.2"},
		{"CIDR membership admits the peer", "192.0.2.0/24", "192.0.2.50:5000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"a mapped IPv6 client is normalized", "127.0.0.1/32", "127.0.0.1:5000", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
		{"several header lines are one chain", "127.0.0.1/32", "127.0.0.1:5000", []string{"1.2.3.4", "203.0.113.7"}, "203.0.113.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustProxies(t, tt.trusted).clientIP(requestFrom(tt.peer, tt.forwarded...))
			if got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The point of the whole change: two clients arriving through one trusted proxy
// get independent source buckets, where reading only the peer would pool them.
func TestAttemptLimitSeesPastATrustedProxy(t *testing.T) {
	limit := &attemptLimit{
		source:   newLimiter(1, time.Minute, 64),
		email:    newLimiter(5, time.Minute, 64),
		clientIP: mustProxies(t, "127.0.0.1/32").clientIP,
	}

	first := requestFrom("127.0.0.1:5000", "203.0.113.1")
	if _, ok := limit.allow(first, "a@example.com"); !ok {
		t.Fatal("the first client was refused inside its burst")
	}
	if _, ok := limit.allow(first, "a@example.com"); ok {
		t.Fatal("the first client's source bucket was not spent")
	}

	// Same proxy, a different forwarded client: its own bucket, so allowed.
	second := requestFrom("127.0.0.1:5000", "203.0.113.2")
	if _, ok := limit.allow(second, "b@example.com"); !ok {
		t.Error("a second client through the same proxy shared the first's bucket")
	}
}

// failLogin posts a wrong password and returns the response.
func failLogin(c *client, email string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.post("/login", url.Values{"email": {email}, "password": {"not the password"}})
}

// checkRefused asserts the shape of a refusal: the status, a Retry-After a
// client can act on, and the route's own page rather than a bare error.
func checkRefused(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	retry := w.Header().Get("Retry-After")
	seconds, err := strconv.Atoi(retry)
	if err != nil || seconds < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
	}

	contains(t, w, "Too many attempts")
	contains(t, w, "<form method=\"post\"")
}

// A run of failed logins from one host is refused before it can go on paying
// for password verifications, and a player elsewhere is unaffected.
func TestLoginIsLimitedPerSource(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	// Distinct addresses, so it is the source limit that trips and not the
	// address one.
	for i := 0; i < loginBurst; i++ {
		w := failLogin(c, "player"+strconv.Itoa(i)+"@example.com")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want %d", i, w.Code, http.StatusUnauthorized)
		}
	}

	checkRefused(t, failLogin(c, "player9@example.com"))

	// Another browser, another address: still able to log in.
	elsewhere := c.otherBrowser()
	w := elsewhere.post("/login", url.Values{
		"email":    {"player@example.com"},
		"password": {"a good long password"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("a login from another source = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
}

// Spreading the guessing over many hosts does not get around it: one address is
// one allowance, wherever the attempts come from.
func TestLoginIsLimitedPerEmail(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	for i := 0; i < loginEmailBurst; i++ {
		w := failLogin(c.otherBrowser(), "player@example.com")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want %d", i, w.Code, http.StatusUnauthorized)
		}
	}

	checkRefused(t, failLogin(c.otherBrowser(), "player@example.com"))

	// And it is that address that is spent, not the host that last tried it.
	last := c.otherBrowser()
	failLogin(last, "player@example.com")
	if w := failLogin(last, "somebody-else@example.com"); w.Code != http.StatusUnauthorized {
		t.Errorf("another address from that host = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// A refusal says nothing about whether the address has an account. It is the
// same page, the same status and the same words either way, which is the whole
// reason Authenticate verifies a decoy for addresses it has never seen.
func TestLoginRefusalDoesNotSayWhetherTheAccountExists(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	stranger := c.otherBrowser()
	for i := 0; i < loginBurst; i++ {
		failLogin(stranger, "nobody"+strconv.Itoa(i)+"@example.com")
	}

	real := failLogin(stranger, "player@example.com")
	fake := failLogin(stranger, "ghost@example.com")

	checkRefused(t, real)
	checkRefused(t, fake)

	// The bodies differ only where the form echoes back what was typed.
	realBody := strings.ReplaceAll(real.Body.String(), "player@example.com", "typed")
	fakeBody := strings.ReplaceAll(fake.Body.String(), "ghost@example.com", "typed")
	if realBody != fakeBody {
		t.Error("the refusal reads differently for an address that has an account")
	}
	for _, leak := range []string{"do not match an account", "already an account"} {
		if strings.Contains(realBody, leak) {
			t.Errorf("the refusal contains %q", leak)
		}
	}
}

// Registration is the same cost as logging in and writes a row besides, so a
// run of it from one host is refused too.
func TestRegistrationIsLimitedPerSource(t *testing.T) {
	c := newTestClient(t)

	// No invitation: refused before anything is hashed, and each attempt still
	// costs a token, which is what is being asserted. There is much less behind
	// the limit than there was — the invitation is checked first, so the
	// Argon2id cost is no longer reachable here — but the route is still open to
	// anyone who can reach the server, so the limit stays.
	for i := 0; i < registerBurst; i++ {
		w := c.post("/register", url.Values{
			"email":    {"player" + strconv.Itoa(i) + "@example.com"},
			"password": {"short"},
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want %d", i, w.Code, http.StatusForbidden)
		}
	}

	checkRefused(t, c.post("/register", url.Values{
		"email":    {"player9@example.com"},
		"password": {"a good long password"},
	}))

	// Somebody else can still sign up.
	c.otherBrowser().register("newcomer@example.com", "a good long password")
}

// One address is one allowance here too, so a list cannot be walked from a
// spread of hosts to find out which of them are registered.
func TestRegistrationIsLimitedPerEmail(t *testing.T) {
	c := newTestClient(t)

	for i := 0; i < registerEmailBurst; i++ {
		w := c.otherBrowser().post("/register", url.Values{
			"email":    {"player@example.com"},
			"password": {"short"},
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want %d", i, w.Code, http.StatusForbidden)
		}
	}

	checkRefused(t, c.otherBrowser().post("/register", url.Values{
		"email":    {"PLAYER@Example.COM"},
		"password": {"short"},
	}))
}
