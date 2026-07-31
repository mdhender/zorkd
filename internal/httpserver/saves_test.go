package httpserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A bare SAVE is a question, not a turn. The player's line is echoed and the
// command line is replaced with the field that asks for a name.
func TestBareSaveAsksForAName(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"save"}})
	if w.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", w.Code, http.StatusOK)
	}

	contains(t, w, "&gt;save")
	contains(t, w, `id="prompt-area"`)
	contains(t, w, `hx-swap-oob="true"`)
	contains(t, w, "Save game as:")
	contains(t, w, `action="/games/`+id+`/saves"`)

	// The story never saw it, so it never said this.
	if strings.Contains(w.Body.String(), "Failed.") {
		t.Error("the story answered the save")
	}
}

// A bare RESTORE shows what there is to choose from.
func TestBareRestoreShowsTheSelector(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	empty := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"restore"}})
	if empty.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", empty.Code, http.StatusOK)
	}
	contains(t, empty, "No saved games yet")

	c.save(id, "before troll")

	listed := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"restore"}})
	contains(t, listed, "Restore which?")
	contains(t, listed, "before troll")
	contains(t, listed, `/saves/1/restore`)
	contains(t, listed, `/saves/1/delete`)
}

// The whole round trip through the browser: save, play on, restore, and find
// the game where it was left.
func TestSaveAndRestoreThroughTheTerminal(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.play(id, "open mailbox")
	c.play(id, "take leaflet")
	c.save(id, "with leaflet")

	c.play(id, "drop leaflet")
	c.play(id, "north")

	// The transcript says both that the save happened and that the game moved
	// on afterwards.
	page := c.get("/games/" + id)
	contains(t, page, `[Saved as &#34;with leaflet&#34;.]`)
	contains(t, page, "drop leaflet")

	saves := c.get("/games/" + id + "/saves")
	if saves.Code != http.StatusOK {
		t.Fatalf("GET saves = %d, want %d", saves.Code, http.StatusOK)
	}
	contains(t, saves, "with leaflet")
	contains(t, saves, "Restore which?")

	restored := c.post("/games/"+id+"/saves/1/restore", nil)
	if restored.Code != http.StatusSeeOther {
		t.Fatalf("POST restore = %d, want %d: %s", restored.Code, http.StatusSeeOther, restored.Body.String())
	}
	if got := restored.Header().Get("Location"); got != "/games/"+id {
		t.Errorf("Location = %q, want %q", got, "/games/"+id)
	}

	back := c.get("/games/" + id)
	contains(t, back, `[Restored &#34;with leaflet&#34;.]`)
	if strings.Contains(back.Body.String(), "drop leaflet") {
		t.Error("the transcript still holds turns the restore undid")
	}

	// And the game itself went back: the leaflet is in hand again.
	inventory := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"inventory"}})
	contains(t, inventory, "leaflet")
}

// A name typed on the line saves without being asked for one.
func TestSaveNamedOnTheCommandLine(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {"save before troll"}})
	if w.Code != http.StatusOK {
		t.Fatalf("POST input = %d, want %d", w.Code, http.StatusOK)
	}
	contains(t, w, "&gt;save before troll")
	contains(t, w, "Saved as &#34;before troll&#34;.")

	// What the turn showed is what a refresh shows.
	page := c.get("/games/" + id)
	contains(t, page, "&gt;save before troll")
	contains(t, page, "[Saved as &#34;before troll&#34;.]")
}

// Deleting is reported, and the save is gone.
func TestDeleteSaveThroughTheTerminal(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")
	c.save(id, "cellar")

	w := c.post("/games/"+id+"/saves/1/delete", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	to := w.Header().Get("Location")
	if !strings.HasPrefix(to, "/games/"+id+"/saves") {
		t.Fatalf("Location = %q, want the saves page", to)
	}

	after := c.get(to)
	contains(t, after, "Deleted &#34;cellar&#34;.")
	contains(t, after, "No saved games yet")
}

// An unusable name is refused and said so in words the player can act on.
func TestSaveRefusesAnUnusableName(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	w := c.post("/games/"+id+"/saves", url.Values{"name": {"   "}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST saves = %d, want %d", w.Code, http.StatusSeeOther)
	}

	page := c.get(w.Header().Get("Location"))
	contains(t, page, "A save name has to be something readable")
	contains(t, page, "Save game as:")
}

// A save name is the player's own words and is escaped like any other
// untrusted text.
func TestSaveNamesAreEscaped(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")

	c.save(id, `<script>alert("x")</script>`)

	page := c.get("/games/" + id + "/saves")
	if strings.Contains(page.Body.String(), "<script>alert") {
		t.Error("a save name reached the page as markup")
	}
	contains(t, page, "&lt;script&gt;")
}

// Every save operation is authorized against the game's owner, and a game that
// is somebody else's reads as missing.
func TestOneUserCannotReachAnothersSaves(t *testing.T) {
	c := newTestClient(t)

	c.register("owner@example.com", "a good long password")
	id := c.startGame("zork1")
	c.save(id, "mine")

	stranger := &client{t: t, handler: c.handler}
	stranger.register("stranger@example.com", "another good password")

	for _, probe := range []struct {
		name string
		do   func() *http.Response
	}{
		{"list", func() *http.Response { return stranger.get("/games/" + id + "/saves").Result() }},
		{"write", func() *http.Response {
			return stranger.post("/games/"+id+"/saves", url.Values{"name": {"theirs"}}).Result()
		}},
		{"restore", func() *http.Response { return stranger.post("/games/"+id+"/saves/1/restore", nil).Result() }},
		{"delete", func() *http.Response { return stranger.post("/games/"+id+"/saves/1/delete", nil).Result() }},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := probe.do().StatusCode; got != http.StatusNotFound {
				t.Errorf("status = %d, want %d", got, http.StatusNotFound)
			}
		})
	}

	// And the owner's save is untouched.
	page := c.get("/games/" + id + "/saves")
	contains(t, page, "mine")
}

// A story that ended itself offers its saves: restoring one is the way back
// from an ending, and it is the only thing left that can be done with the game.
func TestAnEndedGameOffersItsSaves(t *testing.T) {
	c := newTestClient(t)
	c.register("player@example.com", "a good long password")
	id := c.startGame("zork1")
	c.save(id, "alive")

	c.play(id, "quit")
	c.play(id, "yes")

	page := c.get("/games/" + id)
	contains(t, page, "The story has ended.")
	contains(t, page, "Or go back to a save:")
	contains(t, page, "alive")

	// The command line is gone: there is nothing to type at.
	if strings.Contains(page.Body.String(), `id="command"`) {
		t.Error("an ended game still offers a command line")
	}

	restored := c.post("/games/"+id+"/saves/1/restore", nil)
	if restored.Code != http.StatusSeeOther {
		t.Fatalf("POST restore = %d, want %d: %s", restored.Code, http.StatusSeeOther, restored.Body.String())
	}

	back := c.get("/games/" + id)
	contains(t, back, `id="command"`)
	if strings.Contains(back.Body.String(), "The story has ended.") {
		t.Error("the game is still over after restoring a save from before it ended")
	}
}

// save writes a named save and fails the test if it does not take.
func (c *client) save(id, name string) {
	c.t.Helper()

	w := c.post("/games/"+id+"/saves", url.Values{"name": {name}})
	if w.Code != http.StatusSeeOther {
		c.t.Fatalf("POST /games/%s/saves = %d, want %d: %s", id, w.Code, http.StatusSeeOther, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/games/"+id {
		c.t.Fatalf("Location = %q, want %q", got, "/games/"+id)
	}
}

// play submits one command as htmx does.
func (c *client) play(id, command string) {
	c.t.Helper()

	w := c.postHTMX("/games/"+id+"/input", url.Values{"command": {command}})
	if w.Code != http.StatusOK {
		c.t.Fatalf("POST input %q = %d, want %d: %s", command, w.Code, http.StatusOK, w.Body.String())
	}
}
