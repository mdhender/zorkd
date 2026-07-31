package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/session"
)

// A client is a browser: it keeps its cookies and follows nothing
// automatically, so a test can see the redirect it was sent.
//
// Each one connects from its own address. The unauthenticated routes are rate
// limited per source, so two clients that shared an address would spend one
// allowance between them.
type client struct {
	t       *testing.T
	handler http.Handler
	addr    string
	cookies []*http.Cookie
}

// nextAddr hands out a distinct source address. Limiters do not outlive the
// server a test built, so addresses only have to differ within one test.
var addrs atomic.Uint32

func nextAddr() string {
	return fmt.Sprintf("198.51.100.%d:1024", addrs.Add(1)%254+1)
}

func newTestClient(t *testing.T) *client {
	t.Helper()

	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	games, err := game.NewService(library, game.NewRunner(), game.NewMemoryStore())
	if err != nil {
		t.Fatalf("game.NewService() error = %v", err)
	}
	accounts, err := auth.NewService(newAccountStore())
	if err != nil {
		t.Fatalf("auth.NewService() error = %v", err)
	}
	// Plain HTTP under test, so the cookie must survive it.
	sessions, err := session.NewManager(newSessionStore(), session.WithInsecureCookies())
	if err != nil {
		t.Fatalf("session.NewManager() error = %v", err)
	}

	server, err := New(games, accounts, sessions, library, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return &client{t: t, handler: server.Handler(), addr: nextAddr()}
}

// otherBrowser is a second browser on the same server: its own cookies, its own
// address, and no session.
func (c *client) otherBrowser() *client {
	return &client{t: c.t, handler: c.handler, addr: nextAddr()}
}

func (c *client) do(r *http.Request) *httptest.ResponseRecorder {
	c.t.Helper()

	if c.addr != "" {
		r.RemoteAddr = c.addr
	}

	for _, cookie := range c.cookies {
		r.AddCookie(cookie)
	}

	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)

	c.keep(w.Result().Cookies())
	return w
}

func (c *client) keep(cookies []*http.Cookie) {
	for _, fresh := range cookies {
		replaced := false
		for i, held := range c.cookies {
			if held.Name == fresh.Name {
				c.cookies[i] = fresh
				replaced = true
			}
		}
		if !replaced {
			c.cookies = append(c.cookies, fresh)
		}
	}
}

func (c *client) get(path string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func (c *client) post(path string, form url.Values) *httptest.ResponseRecorder {
	c.t.Helper()

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(r)
}

// postHTMX is the same request htmx makes.
func (c *client) postHTMX(path string, form url.Values) *httptest.ResponseRecorder {
	c.t.Helper()

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	return c.do(r)
}

// register creates an account and leaves the client logged in.
func (c *client) register(email, password string) {
	c.t.Helper()

	w := c.post("/register", url.Values{"email": {email}, "password": {password}})
	if w.Code != http.StatusSeeOther {
		c.t.Fatalf("POST /register = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
}

// startGame begins a story and returns the identifier from the redirect.
func (c *client) startGame(story string) string {
	c.t.Helper()

	w := c.post("/games", url.Values{"story": {story}})
	if w.Code != http.StatusSeeOther {
		c.t.Fatalf("POST /games = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	location := w.Header().Get("Location")
	id, ok := strings.CutPrefix(location, "/games/")
	if !ok {
		c.t.Fatalf("Location = %q, want a game", location)
	}
	return id
}

func contains(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()

	if !strings.Contains(w.Body.String(), want) {
		t.Errorf("the response does not contain %q:\n%s", want, w.Body.String())
	}
}

func TestAnonymousRequestsGoToLogin(t *testing.T) {
	c := newTestClient(t)

	for _, path := range []string{"/", "/games/1"} {
		t.Run(path, func(t *testing.T) {
			w := c.get(path)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("GET %s = %d, want %d", path, w.Code, http.StatusSeeOther)
			}
			if got := w.Header().Get("Location"); got != "/login" {
				t.Errorf("Location = %q, want %q", got, "/login")
			}
		})
	}
}

// An htmx request is told to navigate rather than handed a login page to splice
// into the middle of a terminal.
func TestAnonymousHTMXRequestIsToldToRedirect(t *testing.T) {
	c := newTestClient(t)

	w := c.postHTMX("/games/1/input", url.Values{"command": {"look"}})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if got := w.Header().Get("HX-Redirect"); got != "/login" {
		t.Errorf("HX-Redirect = %q, want %q", got, "/login")
	}
}

func TestRegisterThenPlay(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	lobby := c.get("/")
	if lobby.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", lobby.Code, http.StatusOK)
	}
	contains(t, lobby, "player@example.com")
	contains(t, lobby, "Zork I")

	id := c.startGame("zork1")

	page := c.get("/games/" + id)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /games/%s = %d, want %d", id, page.Code, http.StatusOK)
	}
	contains(t, page, "West of House")
	contains(t, page, `id="transcript"`)
	contains(t, page, `hx-post="/games/`+id+`/input"`)
	contains(t, page, "autofocus")

	turn := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"open mailbox"}})
	if turn.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", turn.Code, http.StatusOK)
	}

	body := turn.Body.String()
	contains(t, turn, "open mailbox")
	contains(t, turn, "reveals a leaflet")
	contains(t, turn, `hx-swap-oob="true"`)
	contains(t, turn, "West of House")
	contains(t, turn, "Score:")

	// A fragment, not a document.
	if strings.Contains(body, "<html") {
		t.Error("an htmx request received a whole document")
	}

	// The command is echoed after a prompt and the story's answer follows on
	// the next line, exactly as a terminal shows it.
	if !strings.HasPrefix(body, "&gt;open mailbox\n") {
		t.Errorf("the fragment does not begin with a prompt and the echoed command:\n%q",
			body[:min(60, len(body))])
	}

	// The story's own trailing prompt is not in the fragment: the command
	// field draws it, and two of them would be one too many.
	transcript, _, _ := strings.Cut(body, `<div class="status-bar"`)
	if strings.HasSuffix(transcript, "&gt;") {
		t.Errorf("the fragment ends with a prompt of its own:\n%q", transcript)
	}
}

// A refresh redraws the whole terminal from what is stored. The browser
// remembers nothing.
func TestRefreshRedrawsTheTerminal(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	for _, command := range []string{"open mailbox", "take leaflet", "north"} {
		w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {command}})
		if w.Code != http.StatusOK {
			t.Fatalf("POST %q = %d, want %d", command, w.Code, http.StatusOK)
		}
	}

	page := c.get("/games/" + id)
	body := page.Body.String()

	for _, want := range []string{"West of House", "open mailbox", "take leaflet", "north"} {
		if !strings.Contains(body, want) {
			t.Errorf("the redrawn transcript is missing %q", want)
		}
	}

	// The status bar is redrawn too. It belongs to the turn that reported it,
	// and a refresh plays no turn, so it has to have been stored.
	for _, want := range []string{`id="status-bar"`, "North of House", "Score:", "Moves: 3"} {
		if !strings.Contains(body, want) {
			t.Errorf("the redrawn status bar is missing %q", want)
		}
	}
}

// Without JavaScript the same form still works: it posts, and the browser is
// sent back to a page that redraws from the same stored transcript.
func TestPlayWithoutHTMX(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.post("/games/"+id+"/input", url.Values{"command": {"open mailbox"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST input = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if got := w.Header().Get("Location"); got != "/games/"+id {
		t.Errorf("Location = %q, want %q", got, "/games/"+id)
	}

	contains(t, c.get("/games/"+id), "reveals a leaflet")
}

// Story output is data. Whatever it contains, it is never markup — and neither
// is what the player typed.
func TestStoryOutputAndCommandsAreEscaped(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"<script>alert(1)</script>"}})
	if w.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("the command reached the page as markup:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the command was not escaped:\n%s", body)
	}

	// And it survives into the page a refresh draws.
	page := c.get("/games/" + id).Body.String()
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Error("the command reached the redrawn page as markup")
	}
}

// A game identifier is not a capability. Another player holding one cannot open
// it, and is not told that it exists.
func TestOneUserCannotOpenAnothersGame(t *testing.T) {
	owner := newTestClient(t)
	owner.register("player@example.com", "a good long password")
	id := owner.startGame("zork1")

	// The same server, a different browser and a different account.
	stranger := owner.otherBrowser()
	stranger.register("stranger@example.com", "another good password")

	if w := stranger.get("/games/" + id); w.Code != http.StatusNotFound {
		t.Errorf("GET as another user = %d, want %d", w.Code, http.StatusNotFound)
	}
	if w := stranger.postHTMX("/games/"+id+"/input", url.Values{"command": {"north"}}); w.Code != http.StatusNotFound {
		t.Errorf("POST as another user = %d, want %d", w.Code, http.StatusNotFound)
	}

	// The owner's game did not move.
	contains(t, owner.get("/games/"+id), "West of House")
}

// Typing RESTART asks before it throws anything away, and confirming leaves the
// terminal showing the new game and nothing of the old one.
func TestRestartThroughTheTerminal(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "open mailbox")
	c.play(id, "take leaflet")

	asked := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"restart"}})
	if asked.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", asked.Code, http.StatusOK)
	}
	contains(t, asked, "&gt;restart")
	contains(t, asked, `id="prompt-area"`)
	contains(t, asked, `hx-swap-oob="true"`)
	contains(t, asked, "Restart this story from the beginning?")
	contains(t, asked, `action="/games/`+id+`/restart"`)

	// The question changed nothing: the game is still where it was.
	contains(t, c.get("/games/"+id), "reveals a leaflet")

	restarted := c.post("/games/"+id+"/restart", nil)
	if restarted.Code != http.StatusSeeOther {
		t.Fatalf("POST restart = %d, want %d: %s", restarted.Code, http.StatusSeeOther, restarted.Body.String())
	}
	if got := restarted.Header().Get("Location"); got != "/games/"+id {
		t.Errorf("Location = %q, want %q", got, "/games/"+id)
	}

	page := c.get("/games/" + id)
	body := page.Body.String()
	for _, gone := range []string{"take leaflet", "reveals a leaflet"} {
		if strings.Contains(body, gone) {
			t.Errorf("the redrawn terminal still holds %q from the abandoned game", gone)
		}
	}
	contains(t, page, "West of House")
	contains(t, page, "Moves: 0")
	contains(t, page, `id="command"`)
}

// Without JavaScript the same command still works: the confirmation is drawn on
// the game page, with the game about to be thrown away still above it.
func TestRestartWithoutHTMX(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.post("/games/"+id+"/input", url.Values{"command": {"restart"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST input = %d, want %d", w.Code, http.StatusSeeOther)
	}

	to := w.Header().Get("Location")
	if to != "/games/"+id+"?prompt=restart" {
		t.Fatalf("Location = %q, want the restart confirmation", to)
	}

	page := c.get(to)
	contains(t, page, "Restart this story from the beginning?")
	contains(t, page, `action="/games/`+id+`/restart"`)
	contains(t, page, "West of House")

	// Cancelling is a link back to the game, which still has its command line.
	contains(t, c.get("/games/"+id), `id="command"`)
}

// An htmx client is told to navigate rather than handed a fragment: the whole
// transcript was replaced, so the screen it would splice into is gone.
func TestRestartTellsHTMXToRedraw(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")
	c.play(id, "open mailbox")

	w := c.postHTMX("/games/"+id+"/restart", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("POST restart = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("HX-Redirect"); got != "/games/"+id {
		t.Errorf("HX-Redirect = %q, want %q", got, "/games/"+id)
	}

	if strings.Contains(c.get("/games/"+id).Body.String(), "reveals a leaflet") {
		t.Error("the terminal still holds the abandoned game")
	}
}

// Restarting is authorized against the game's owner like everything else, and a
// game that is somebody else's reads as missing.
func TestOneUserCannotRestartAnothersGame(t *testing.T) {
	owner := newTestClient(t)
	owner.register("player@example.com", "a good long password")
	id := owner.startGame("zork1")
	owner.play(id, "open mailbox")

	stranger := owner.otherBrowser()
	stranger.register("stranger@example.com", "another good password")

	if w := stranger.post("/games/"+id+"/restart", nil); w.Code != http.StatusNotFound {
		t.Errorf("POST restart as another user = %d, want %d", w.Code, http.StatusNotFound)
	}

	// And the owner's game did not go back to the beginning.
	contains(t, owner.get("/games/"+id), "reveals a leaflet")
}

// A story that ended itself is exactly the one a player wants to begin again,
// so the ended terminal offers it and the route does not refuse.
func TestAnEndedGameCanBeRestarted(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "quit")
	c.play(id, "yes")

	ended := c.get("/games/" + id)
	contains(t, ended, "The story has ended.")
	contains(t, ended, "/games/"+id+"?prompt=restart")

	confirm := c.get("/games/" + id + "?prompt=restart")
	contains(t, confirm, "Restart this story from the beginning?")

	w := c.post("/games/"+id+"/restart", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST restart = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	back := c.get("/games/" + id)
	contains(t, back, `id="command"`)
	contains(t, back, "West of House")
	if strings.Contains(back.Body.String(), "The story has ended.") {
		t.Error("the game is still over after restarting it")
	}
}

// A restart throws away the game in progress and nothing else: the saves are
// still there, and still restore.
func TestRestartKeepsSavesThroughTheTerminal(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "open mailbox")
	c.play(id, "take leaflet")
	c.save(id, "with leaflet")

	if w := c.post("/games/"+id+"/restart", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST restart = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	saves := c.get("/games/" + id + "/saves")
	contains(t, saves, "with leaflet")

	if w := c.post("/games/"+id+"/saves/1/restore", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST restore = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	inventory := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"inventory"}})
	contains(t, inventory, "leaflet")
}

// The whole deletion, the way a player without JavaScript walks it: a link in
// the lobby, a confirmation on the game's own page, and a post that returns to
// the lobby with the game gone.
func TestDeleteGameFromTheLobby(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "open mailbox")
	c.save(id, "with mailbox open")

	lobby := c.get("/")
	contains(t, lobby, `href="/games/`+id+`?prompt=delete"`)

	confirm := c.get("/games/" + id + "?prompt=delete")
	if confirm.Code != http.StatusOK {
		t.Fatalf("GET the confirmation = %d, want %d", confirm.Code, http.StatusOK)
	}
	contains(t, confirm, "Delete this game for good?")
	contains(t, confirm, "Every saved game it holds goes with it")
	contains(t, confirm, `action="/games/`+id+`/delete"`)
	// The game about to be thrown away is on the screen while the question is
	// asked, and so is the way out of it.
	contains(t, confirm, "West of House")
	contains(t, confirm, "Cancel")

	// Asking changed nothing.
	if w := c.get("/games/" + id); w.Code != http.StatusOK {
		t.Fatalf("GET the game after the question = %d, want %d", w.Code, http.StatusOK)
	}
	contains(t, c.get("/games/"+id+"/saves"), "with mailbox open")

	w := c.post("/games/"+id+"/delete", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	to := w.Header().Get("Location")
	if !strings.HasPrefix(to, "/?") {
		t.Fatalf("Location = %q, want the lobby", to)
	}

	after := c.get(to)
	contains(t, after, "That game is gone")
	if strings.Contains(after.Body.String(), `href="/games/`+id+`"`) {
		t.Errorf("the lobby still lists the deleted game:\n%s", after.Body.String())
	}
	contains(t, after, "No games yet.")

	// And it is gone, not merely unlisted.
	if w := c.get("/games/" + id); w.Code != http.StatusNotFound {
		t.Errorf("GET the deleted game = %d, want %d", w.Code, http.StatusNotFound)
	}
	if w := c.post("/games/"+id+"/delete", nil); w.Code != http.StatusNotFound {
		t.Errorf("POST delete twice = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// A story that ended itself is the one a player is most likely to want rid of,
// so the confirmation is offered ahead of the ended prompt rather than behind
// it.
func TestAnEndedGameCanBeDeleted(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "quit")
	c.play(id, "yes")

	confirm := c.get("/games/" + id + "?prompt=delete")
	contains(t, confirm, "Delete this game for good?")
	contains(t, confirm, `action="/games/`+id+`/delete"`)

	if w := c.post("/games/"+id+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if w := c.get("/games/" + id); w.Code != http.StatusNotFound {
		t.Errorf("GET the deleted game = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// An htmx client is told to navigate: the page it posted from describes a game
// that no longer exists, so there is no fragment to splice into it.
func TestDeleteGameTellsHTMXToNavigate(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.postHTMX("/games/"+id+"/delete", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("POST delete = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if to := w.Header().Get("HX-Redirect"); !strings.HasPrefix(to, "/?") {
		t.Errorf("HX-Redirect = %q, want the lobby", to)
	}

	if w := c.get("/games/" + id); w.Code != http.StatusNotFound {
		t.Errorf("GET the deleted game = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// Deleting is authorized against the game's owner like everything else, and a
// game that is somebody else's reads as missing.
func TestOneUserCannotDeleteAnothersGame(t *testing.T) {
	owner := newTestClient(t)
	owner.register("player@example.com", "a good long password")
	id := owner.startGame("zork1")
	owner.play(id, "open mailbox")

	stranger := owner.otherBrowser()
	stranger.register("stranger@example.com", "another good password")

	if w := stranger.get("/games/" + id + "?prompt=delete"); w.Code != http.StatusNotFound {
		t.Errorf("GET the confirmation as another user = %d, want %d", w.Code, http.StatusNotFound)
	}
	if w := stranger.post("/games/"+id+"/delete", nil); w.Code != http.StatusNotFound {
		t.Errorf("POST delete as another user = %d, want %d", w.Code, http.StatusNotFound)
	}

	// And the owner's game survived it.
	contains(t, owner.get("/games/"+id), "reveals a leaflet")
}

func TestLoginAndLogout(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	if w := c.post("/logout", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d, want %d", w.Code, http.StatusSeeOther)
	}

	// Logged out: the cookie the client still holds is worth nothing.
	if w := c.get("/"); w.Code != http.StatusSeeOther {
		t.Errorf("GET / after logout = %d, want a redirect to login", w.Code)
	}

	bad := c.post("/login", url.Values{"email": {"player@example.com"}, "password": {"the wrong one"}})
	if bad.Code != http.StatusUnauthorized {
		t.Errorf("POST /login with a wrong password = %d, want %d", bad.Code, http.StatusUnauthorized)
	}
	contains(t, bad, "do not match an account")

	good := c.post("/login", url.Values{"email": {"PLAYER@example.com"}, "password": {"a good long password"}})
	if good.Code != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want %d", good.Code, http.StatusSeeOther)
	}
	if w := c.get("/"); w.Code != http.StatusOK {
		t.Errorf("GET / after logging in = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRegisterReportsBadInput(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")

	fresh := c.otherBrowser()

	tests := []struct {
		name     string
		email    string
		password string
		want     string
	}{
		{"taken", "player@example.com", "a good long password", "already an account"},
		{"not an address", "player", "a good long password", "does not look like an email"},
		{"short password", "other@example.com", "short", "not long enough"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := fresh.post("/register", url.Values{"email": {tt.email}, "password": {tt.password}})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
			contains(t, w, tt.want)

			// The address is offered back; the password never is.
			if tt.email != "" && !strings.Contains(w.Body.String(), tt.email) {
				t.Error("the address was not echoed back into the form")
			}
			if strings.Contains(w.Body.String(), tt.password) {
				t.Error("the password was echoed back into the page")
			}
		})
	}
}

// A command longer than a Version 3 text buffer holds is refused before the
// engine sees it, and the terminal says so.
func TestOverlongCommandIsRefused(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	long := strings.Repeat("x", game.MaxCommandBytes+1)

	w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {long}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	contains(t, w, "too long")

	// And the game did not move.
	contains(t, c.get("/games/"+id), "West of House")
}

// Cross-origin protection refuses a state-changing request that a browser says
// came from somewhere else.
func TestCrossOriginPostIsRefused(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	r := httptest.NewRequest(http.MethodPost, "/games/"+id+"/input",
		strings.NewReader(url.Values{"command": {"north"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	if w := c.do(r); w.Code != http.StatusForbidden {
		t.Errorf("a cross-site POST = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// The polish is presentation, and it is all delivered by the page rather than
// asked for from anybody else.
func TestTerminalPolishIsOnThePage(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	page := c.get("/games/" + id)
	body := page.Body.String()

	// Command history, scoped to this game.
	for _, want := range []string{`x-data="prompt('` + id + `')"`, "earlier()", "later()"} {
		if !strings.Contains(body, want) {
			t.Errorf("the command line is missing %q", want)
		}
	}

	// The screen preferences, on the game page and on the lobby.
	for _, page := range []*httptest.ResponseRecorder{page, c.get("/")} {
		for _, want := range []string{`x-data="preferences"`, "amber", "scanlines"} {
			if !strings.Contains(page.Body.String(), want) {
				t.Errorf("a page is missing the %q preference", want)
			}
		}
	}

	// The preference is applied before the first paint, or a player who chose
	// amber watches the screen flash green on every page.
	if !strings.Contains(body, "zorkd.phosphor") {
		t.Error("the page does not apply the stored phosphor before painting")
	}

	// Nothing is fetched from anywhere but this server.
	for _, forbidden := range []string{"//unpkg.com", "//cdn.", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the page reaches outside this server: %q", forbidden)
		}
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	c := newTestClient(t)

	for _, path := range []string{
		"/static/terminal.css",
		"/static/terminal.js",
		"/static/vendor/htmx.min.js",
		"/static/vendor/alpine.min.js",
	} {
		t.Run(path, func(t *testing.T) {
			w := c.get(path)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", path, w.Code, http.StatusOK)
			}
			if w.Body.Len() == 0 {
				t.Error("the asset is empty")
			}
		})
	}
}

func TestNewRequiresItsParts(t *testing.T) {
	library, err := game.Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	games, err := game.NewService(library, game.NewRunner(), game.NewMemoryStore())
	if err != nil {
		t.Fatalf("game.NewService() error = %v", err)
	}
	accounts, err := auth.NewService(newAccountStore())
	if err != nil {
		t.Fatalf("auth.NewService() error = %v", err)
	}
	sessions, err := session.NewManager(newSessionStore())
	if err != nil {
		t.Fatalf("session.NewManager() error = %v", err)
	}

	tests := []struct {
		name     string
		games    *game.Service
		accounts *auth.Service
		sessions *session.Manager
		library  *game.Library
	}{
		{name: "no game service", accounts: accounts, sessions: sessions, library: library},
		{name: "no auth service", games: games, sessions: sessions, library: library},
		{name: "no session manager", games: games, accounts: accounts, library: library},
		{name: "no library", games: games, accounts: accounts, sessions: sessions},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.games, tt.accounts, tt.sessions, tt.library, nil); err == nil {
				t.Fatal("New() = nil error, want failure")
			}
		})
	}
}
