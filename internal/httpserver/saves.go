package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mdhender/zorkd/internal/game"
)

// The named-save surface.
//
// The forms here are ordinary posts rather than htmx ones, and deliberately so:
// writing a save and restoring one both change the whole screen — a restore
// replaces the transcript outright — so the answer is a redirect and a redraw
// rather than a fragment spliced into the terminal. The terminal keeps its live
// feel where it matters, which is playing turns.

// showSaves draws the terminal with the save prompt or the restore selector
// under it.
//
// It is the same page the game is played on, so cancelling puts the player back
// exactly where they were rather than in a menu they have to find their way out
// of.
func (s *Server) showSaves(w http.ResponseWriter, r *http.Request, player user) {
	mode := "restore"
	if r.URL.Query().Get("prompt") == "save" {
		mode = "save"
	}

	s.drawGame(w, r, player, r.PathValue("id"), mode)
}

// createSave writes the game's state under the name the player typed.
func (s *Server) createSave(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	if err := parseForm(w, r); err != nil {
		s.saveFailed(w, r, sessionID, "save", errors.New("that request could not be read"))
		return
	}

	if _, _, err := s.games.Save(r.Context(), player.ID, sessionID, r.PostFormValue("name")); err != nil {
		s.saveFailed(w, r, sessionID, "save", err)
		return
	}

	// The game service already wrote the line about it into the transcript, so
	// the redraw says so without a notice above it.
	s.redrawGame(w, r, sessionID)
}

// restoreSave promotes a save to the game's active state.
func (s *Server) restoreSave(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	if _, _, err := s.games.Restore(r.Context(), player.ID, sessionID, r.PathValue("save")); err != nil {
		s.saveFailed(w, r, sessionID, "restore", err)
		return
	}

	s.redrawGame(w, r, sessionID)
}

// deleteSave removes one save.
//
// It is a POST rather than a DELETE because a browser with no JavaScript can
// only send GET and POST from a form, and every route here has to work in one.
func (s *Server) deleteSave(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	deleted, err := s.games.DeleteSave(r.Context(), player.ID, sessionID, r.PathValue("save"))
	if err != nil {
		s.saveFailed(w, r, sessionID, "restore", err)
		return
	}

	s.redirectToSaves(w, r, sessionID, "restore", fmt.Sprintf("Deleted %q.", deleted.Name))
}

// askAbout asks the question a bare SAVE or RESTORE left open.
//
// The player's line is echoed into the transcript and the prompt underneath it
// is replaced, so the terminal reads as one conversation rather than jumping to
// a page about saves.
func (s *Server) askAbout(w http.ResponseWriter, r *http.Request, player user, sessionID, command, mode string) {
	if !isHTMX(r) {
		s.redirectToSaves(w, r, sessionID, mode, "")
		return
	}

	prompt := newPromptView(sessionID, mode, false)

	saves, err := s.games.Saves(r.Context(), player.ID, sessionID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prompt.Saves = saveRows(saves)

	s.renderFragment(w, r, http.StatusOK,
		fragment{"turn", struct{ Command, Output string }{command, ""}},
		fragment{"prompt-area-oob", prompt},
	)
}

// saveFailed reports a save or restore that did not happen.
func (s *Server) saveFailed(w http.ResponseWriter, r *http.Request, sessionID, mode string, err error) {
	switch {
	case errors.Is(err, game.ErrSessionNotFound):
		http.NotFound(w, r)
		return

	case errors.Is(err, game.ErrSaveNotFound):
		s.redirectToSaves(w, r, sessionID, mode, "There is no save by that name.")
		return

	case errors.Is(err, game.ErrInvalidSaveName):
		s.redirectToSaves(w, r, sessionID, mode,
			fmt.Sprintf("A save name has to be something readable, and no longer than %d characters.",
				game.MaxSaveNameBytes))
		return

	case errors.Is(err, game.ErrTooManySaves):
		s.redirectToSaves(w, r, sessionID, mode,
			fmt.Sprintf("This game already holds %d saves. Delete one first.", game.MaxSavesPerGame))
		return

	case errors.Is(err, game.ErrGameOver):
		s.redirectToSaves(w, r, sessionID, mode, "The story has ended; there is nothing to save.")
		return

	case errors.Is(err, game.ErrVersionConflict):
		s.redirectToSaves(w, r, sessionID, mode, "This game moved on somewhere else. Reload to catch up.")
		return

	case errors.Is(err, game.ErrSaveMismatch):
		// The save and the game disagree about which story they belong to.
		// Restoring it would hand the engine bytes that do not describe its
		// memory, so it is refused here rather than reported by the engine.
		s.logger.ErrorContext(r.Context(), "a save names a different story than its game",
			"session", sessionID, "error", err)
		s.redirectToSaves(w, r, sessionID, mode, "That save does not belong to this story.")
		return
	}

	s.fail(w, r, err)
}

// redirectToSaves sends the browser to the save prompt or the restore selector.
func (s *Server) redirectToSaves(w http.ResponseWriter, r *http.Request, sessionID, mode, notice string) {
	query := url.Values{}
	if mode == "save" {
		query.Set("prompt", "save")
	}
	if notice != "" {
		query.Set("notice", notice)
	}

	to := url.URL{Path: "/games/" + sessionID + "/saves", RawQuery: query.Encode()}
	s.sendTo(w, r, to.String())
}

// redrawGame sends the browser back to the terminal to draw it again.
//
// Saving and restoring both change what a page load would produce — a restore
// replaces the transcript entirely — so htmx is told to navigate rather than
// handed a fragment to splice into a screen that is no longer current.
func (s *Server) redrawGame(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.sendTo(w, r, "/games/"+sessionID)
}

// sendTo navigates the browser, whether or not htmx is driving it.
func (s *Server) sendTo(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// promptMode names the question a bare SAVE or RESTORE asks.
func promptMode(intent game.Intent) string {
	if intent == game.IntentSave {
		return "save"
	}
	return "restore"
}
