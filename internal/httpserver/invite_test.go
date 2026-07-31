package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdhender/zorkd/internal/invite"
)

// expiredInvite writes an invitation that was already past its window, which is
// how a test holds one without waiting out a TTL.
func (c *client) expiredInvite(email string) string {
	c.t.Helper()

	token := "expired-" + strconv.Itoa(int(addrs.Add(1)))
	sum := sha256.Sum256([]byte(token))

	err := c.invited.CreateInvitation(context.Background(), sum[:], invite.Invitation{
		Email:     email,
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		c.t.Fatalf("CreateInvitation() error = %v", err)
	}
	return token
}

// The form is not reachable without a usable invitation. "Supply a token to
// submit" means there is nothing on the page to submit.
func TestRegisterFormNeedsAnInvitation(t *testing.T) {
	c := newTestClient(t)

	// One that has been spent, by using it.
	spent := c.invite("first@example.com")
	c.registerWith(spent, "first@example.com", "a good long password")

	fresh := c.otherBrowser()

	tests := []struct {
		name string
		path string
	}{
		{name: "no token", path: "/register"},
		{name: "empty token", path: "/register?invite="},
		{name: "unknown token", path: "/register?invite=not-a-token-anybody-issued"},
		{name: "expired token", path: "/register?invite=" + fresh.expiredInvite("player@example.com")},
		{name: "redeemed token", path: "/register?invite=" + spent},
	}

	var refusals []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := fresh.get(tt.path)
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET %s = %d, want %d", tt.path, w.Code, http.StatusForbidden)
			}
			if strings.Contains(w.Body.String(), "<form") {
				t.Error("the refusal carries a form to submit")
			}
			contains(t, w, "invitation cannot be used")
			refusals = append(refusals, w.Body.String())
		})
	}

	// A refusal says one thing, whatever was wrong with the invitation.
	for i, body := range refusals[1:] {
		if body != refusals[0] {
			t.Errorf("the refusal for %q reads differently from the one for %q",
				tests[i+1].name, tests[0].name)
		}
	}
}

// A usable invitation draws the form, with the address it was issued for on it
// and the token riding through as a hidden field.
func TestRegisterFormShowsTheInvitedAddress(t *testing.T) {
	c := newTestClient(t)
	token := c.invite("Player@Example.COM")

	w := c.get("/register?invite=" + token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /register = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	contains(t, w, `<form`)
	contains(t, w, `name="invite" value="`+token+`"`)
	// The normalized address, because that is the one the invitation is for.
	contains(t, w, `value="player@example.com"`)
	// Shown, and not offered to be changed.
	contains(t, w, "readonly")
}

// The POST check is the one that matters, and it does not trust that the GET
// happened: the address is rechecked against the invitation whatever the
// browser sends.
func TestRegisterRefusesAnAddressTheInvitationIsNotFor(t *testing.T) {
	c := newTestClient(t)
	token := c.invite("player@example.com")

	w := c.post("/register", url.Values{
		"invite":   {token},
		"email":    {"stranger@example.com"},
		"password": {"a good long password"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST /register = %d, want %d: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
	contains(t, w, "invitation cannot be used")

	// Nothing was created, and nothing was spent: the invitation still works
	// for the address it was issued for.
	c.otherBrowser().registerWith(token, "player@example.com", "a good long password")
}

// Both sides are normalized, so an address that differs only in case or in
// surrounding space is the same invitation rather than a mismatch nobody can
// see.
func TestRegisterAcceptsTheAddressHoweverItIsSpelled(t *testing.T) {
	for _, typed := range []string{
		"player@example.com",
		"PLAYER@EXAMPLE.COM",
		"  Player@Example.Com  ",
	} {
		t.Run(typed, func(t *testing.T) {
			c := newTestClient(t)
			token := c.invite("Player@Example.COM")

			c.registerWith(token, typed, "a good long password")

			// Logged in, under the normalized address.
			lobby := c.get("/")
			if lobby.Code != http.StatusOK {
				t.Fatalf("GET / = %d, want %d", lobby.Code, http.StatusOK)
			}
			contains(t, lobby, "player@example.com")
		})
	}
}

// Redeeming spends the invitation: the same link cannot make a second account,
// and it no longer draws a form.
func TestRegisterSpendsTheInvitation(t *testing.T) {
	c := newTestClient(t)
	token := c.invite("player@example.com")

	c.registerWith(token, "player@example.com", "a good long password")

	again := c.otherBrowser()
	if w := again.get("/register?invite=" + token); w.Code != http.StatusForbidden {
		t.Errorf("GET the spent link = %d, want %d", w.Code, http.StatusForbidden)
	}

	w := again.post("/register", url.Values{
		"invite":   {token},
		"email":    {"player@example.com"},
		"password": {"another good password"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST with the spent invitation = %d, want %d: %s",
			w.Code, http.StatusForbidden, w.Body.String())
	}
}

// Two registrations racing on one token produce exactly one account. The
// invitation is spent and the account created in one operation, so the loser is
// refused rather than making a second one.
func TestConcurrentRegistrationsOnOneInvitationMakeOneAccount(t *testing.T) {
	c := newTestClient(t)
	token := c.invite("player@example.com")

	// Each browser has its own cookies and its own source address, so neither
	// waits on the other and neither spends the other's rate-limit allowance.
	const racers = 2
	browsers := make([]*client, racers)
	for i := range browsers {
		browsers[i] = c.otherBrowser()
	}

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		succeeded atomic.Int64
		mu        sync.Mutex
		codes     []int
	)

	start.Add(1)
	for _, browser := range browsers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()

			w := browser.post("/register", url.Values{
				"invite":   {token},
				"email":    {"player@example.com"},
				"password": {"a good long password"},
			})
			if w.Code == http.StatusSeeOther {
				succeeded.Add(1)
			}

			mu.Lock()
			defer mu.Unlock()
			codes = append(codes, w.Code)
		}()
	}

	start.Done()
	done.Wait()

	if got := succeeded.Load(); got != 1 {
		t.Fatalf("%d registrations succeeded, want exactly 1 (statuses %v)", got, codes)
	}

	// The one that lost was refused for the invitation rather than told the
	// address was taken: the invitation is spent inside the same operation that
	// creates the account, so the loser never reaches the account at all.
	for _, code := range codes {
		if code != http.StatusSeeOther && code != http.StatusForbidden {
			t.Errorf("a losing registration = %d, want %d", code, http.StatusForbidden)
		}
	}

	// And the account exists once: logging in works, and the address is not
	// free to register again.
	fresh := c.otherBrowser()
	login := fresh.post("/login", url.Values{
		"email": {"player@example.com"}, "password": {"a good long password"}})
	if login.Code != http.StatusSeeOther {
		t.Errorf("POST /login = %d, want %d", login.Code, http.StatusSeeOther)
	}
}

// This gates registration and nothing else. An account that already exists logs
// in without an invitation, before and after one would have expired.
func TestLoginNeedsNoInvitation(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	if w := c.post("/logout", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d, want %d", w.Code, http.StatusSeeOther)
	}

	// Every invitation in the store is gone; the account is not.
	if _, err := c.invitations.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	w := c.post("/login", url.Values{
		"email": {"player@example.com"}, "password": {"a good long password"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if lobby := c.get("/"); lobby.Code != http.StatusOK {
		t.Errorf("GET / after logging in = %d, want %d", lobby.Code, http.StatusOK)
	}
}

// The token arrives on a query string, and the request log records the path and
// not the query. A token and an email address are both things this project has
// decided not to write down.
func TestTheInvitationTokenDoesNotReachTheLog(t *testing.T) {
	var logged bytes.Buffer
	c := newLoggingTestClient(t, slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))

	token := c.invite("player@example.com")

	if w := c.get("/register?invite=" + token); w.Code != http.StatusOK {
		t.Fatalf("GET /register = %d, want %d", w.Code, http.StatusOK)
	}
	c.registerWith(token, "player@example.com", "a good long password")

	// Refusals are logged too, and a refused token must not be written down
	// either.
	c.otherBrowser().get("/register?invite=" + token)

	written := logged.String()
	if written == "" {
		t.Fatal("nothing was logged, so this test proves nothing")
	}
	if !strings.Contains(written, "/register") {
		t.Fatalf("the register requests were not logged:\n%s", written)
	}
	for _, secret := range []string{token, "player@example.com", "a good long password"} {
		if strings.Contains(written, secret) {
			t.Errorf("the log contains %q:\n%s", secret, written)
		}
	}
}
