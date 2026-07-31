package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/maloquacious/zmachine"

	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/terminal"
)

// lobbyView is the list of a player's games and the stories they can start.
type lobbyView struct {
	Email   string
	Notice  string
	Games   []gameRow
	Stories []storyRow
}

type gameRow struct {
	ID     string
	Story  string
	Turn   int
	Halted bool
}

type storyRow struct {
	ID    string
	Title string
}

// gameView is one terminal.
type gameView struct {
	ID          string
	Story       string
	Transcript  string
	UpperWindow string
	Status      statusView
	Notice      string

	// Prompt is what sits under the transcript: the command line, or one of
	// the questions this application asks in its place.
	Prompt promptView
}

// promptView is the bottom of the terminal.
//
// It is a view of its own because a turn can replace it without redrawing the
// page: typing SAVE swaps the command line for a field asking for a name, and
// the same markup has to come out of a page load and out of a turn.
type promptView struct {
	ID string

	// Mode is "" for the command line, "save" for the name field, or
	// "restore" for the selector.
	Mode string

	Halted      bool
	MaxCommand  int
	MaxSaveName int
	Saves       []saveRow
}

// saveRow is one named save on the selector.
type saveRow struct {
	ID      string
	Name    string
	Turn    int
	Created string
}

// statusView is the status bar the engine reported.
//
// Available is checked by the template before anything else is drawn: every
// other field is meaningless until it is true, and a bar built from them would
// be showing something that was never so.
type statusView struct {
	Available bool
	Name      string
	Score     int16
	Moves     int16
	TimeGame  bool
	Time      string
}

// newStatusView renders the bar from a turn that just happened.
func newStatusView(status zmachine.StatusLine) statusView {
	return storedStatusView(game.StatusLine{
		Available: status.Available,
		Name:      status.Name,
		TimeGame:  status.TimeGame,
		Score:     status.Score,
		Moves:     status.Turns,
		Hours:     status.Hours,
		Minutes:   status.Minutes,
	})
}

// storedStatusView renders the bar from what the last turn left behind, which
// is what a browser that refreshed has to work from.
func storedStatusView(status game.StatusLine) statusView {
	view := statusView{
		Available: status.Available,
		Name:      status.Name,
		Score:     status.Score,
		Moves:     status.Moves,
		TimeGame:  status.TimeGame,
	}
	if status.TimeGame {
		view.Time = terminal.Clock(zmachine.StatusLine{
			TimeGame: true,
			Hours:    status.Hours,
			Minutes:  status.Minutes,
		})
	}
	return view
}

func (s *Server) showLobby(w http.ResponseWriter, r *http.Request, player user) {
	summaries, err := s.games.Games(r.Context(), player.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	view := lobbyView{
		Email:  player.Email,
		Notice: r.URL.Query().Get("notice"),
	}

	for _, summary := range summaries {
		row := gameRow{ID: summary.ID, Story: "unknown story", Turn: summary.Turn, Halted: summary.Halted}
		if entry, ok := s.library.ByKey(summary.StoryKey); ok {
			row.Story = entry.Title
		}
		view.Games = append(view.Games, row)
	}

	for _, entry := range s.library.All() {
		view.Stories = append(view.Stories, storyRow{ID: entry.ID, Title: entry.Title})
	}

	s.renderPage(w, r, "lobby.html", http.StatusOK, view)
}

func (s *Server) newGame(w http.ResponseWriter, r *http.Request, player user) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "That request could not be read.", http.StatusBadRequest)
		return
	}

	session, _, err := s.games.NewGame(r.Context(), player.ID, r.PostFormValue("story"))
	if err != nil {
		if errors.Is(err, game.ErrStoryUnavailable) {
			http.Error(w, "This server does not carry that story.", http.StatusNotFound)
			return
		}
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/games/"+session.ID, http.StatusSeeOther)
}

// showGame draws the whole terminal from what is stored.
//
// This is what a refresh and a fresh login both do. The state is the server's,
// so the screen is rebuilt rather than remembered by the browser.
//
// The restart and delete confirmations are this same page with a different
// prompt under it. Restart is where a browser with no JavaScript is sent when
// the player types RESTART; delete is where the lobby's link goes. Both ask
// about the whole game, so the game is on the screen while the question is
// asked. The save questions live on the saves route with the list they need.
func (s *Server) showGame(w http.ResponseWriter, r *http.Request, player user) {
	mode := ""
	switch r.URL.Query().Get("prompt") {
	case "restart":
		mode = "restart"
	case "delete":
		mode = "delete"
	}

	s.drawGame(w, r, player, r.PathValue("id"), mode)
}

// drawGame renders the terminal with the prompt in the given mode.
func (s *Server) drawGame(w http.ResponseWriter, r *http.Request, player user, sessionID, mode string) {
	stored, err := s.games.Session(r.Context(), player.ID, sessionID)
	if err != nil {
		if errors.Is(err, game.ErrSessionNotFound) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}

	view := gameView{
		ID:         stored.ID,
		Story:      "Zork",
		Transcript: trimPrompt(stored.Transcript),
		Status:     storedStatusView(stored.Status),
		Notice:     r.URL.Query().Get("notice"),
		Prompt:     newPromptView(stored.ID, mode, stored.Halted),
	}
	if entry, ok := s.library.ByKey(stored.StoryKey); ok {
		view.Story = entry.Title
	}

	// The saves are read only when something is going to show them. An ended
	// game shows them too, because restoring one is the way back from an
	// ending. The restart confirmation shows none: they survive it.
	if mode == "save" || mode == "restore" || stored.Halted {
		saves, err := s.games.Saves(r.Context(), player.ID, stored.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		view.Prompt.Saves = saveRows(saves)
	}

	// The upper window is not kept: it is a screen overlay that belongs to the
	// turn that drew it, and Zork does not use one. The transcript and the
	// status bar are what a refresh needs.
	s.renderPage(w, r, "game.html", http.StatusOK, view)
}

// newPromptView is the bottom of the terminal in one of its three states.
func newPromptView(sessionID, mode string, halted bool) promptView {
	return promptView{
		ID:          sessionID,
		Mode:        mode,
		Halted:      halted,
		MaxCommand:  game.MaxCommandBytes,
		MaxSaveName: game.MaxSaveNameBytes,
	}
}

// saveRows renders the saves for the selector.
func saveRows(saves []game.Save) []saveRow {
	rows := make([]saveRow, 0, len(saves))
	for _, save := range saves {
		rows = append(rows, saveRow{
			ID:      save.ID,
			Name:    save.Name,
			Turn:    save.Turn,
			Created: save.CreatedAt.Local().Format("2 Jan 2006, 15:04"),
		})
	}
	return rows
}

// playTurn plays one command and returns what it added to the screen.
//
// An HTMX request gets the fragment. Ordinary navigation — a browser with no
// JavaScript, or a form submitted before the script loaded — gets a redirect
// back to the page, which redraws from the same stored transcript.
func (s *Server) playTurn(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	if err := parseForm(w, r); err != nil {
		s.answerTurn(w, r, sessionID, "", "That request could not be read.")
		return
	}

	// A command is one line. Anything a browser sent past the first is not
	// something the player typed into a single-line field.
	command := strings.TrimSpace(strings.SplitN(r.PostFormValue("command"), "\n", 2)[0])

	if len(command) > game.MaxCommandBytes {
		s.answerTurn(w, r, sessionID, command, "That command is too long.")
		return
	}

	turn, err := s.games.Play(r.Context(), player.ID, sessionID, command)
	if err != nil {
		s.turnFailed(w, r, sessionID, command, err)
		return
	}

	// SAVE, RESTORE and RESTART were answered by the game service and never
	// reached the engine. A bare one is a question this application still has to
	// ask.
	switch {
	case turn.Asked:
		s.askAbout(w, r, player, sessionID, command, promptMode(turn.Intent))
		return

	case turn.Intent == game.IntentSave:
		s.answerTurn(w, r, sessionID, command, turn.Notice())
		return

	case turn.Intent == game.IntentRestore:
		// The whole transcript went back with the state, so there is nothing to
		// append: the screen is redrawn from what is now stored.
		s.redrawGame(w, r, sessionID)
		return
	}

	if isHTMX(r) {
		s.renderFragment(w, r, http.StatusOK,
			fragment{"turn", struct{ Command, Output string }{command, trimPrompt(turn.Result.Output)}},
			fragment{"status-bar-oob", newStatusView(turn.Result.StatusLine)},
			fragment{"upper-window-oob", turn.Result.UpperWindow},
		)
		return
	}

	http.Redirect(w, r, "/games/"+sessionID, http.StatusSeeOther)
}

// restartGame begins the story again, once the player has confirmed.
//
// It is a POST for the same reason deletion is: a form is all a browser without
// JavaScript can send, and this throws a game away.
func (s *Server) restartGame(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	if _, err := s.games.Restart(r.Context(), player.ID, sessionID); err != nil {
		s.turnFailed(w, r, sessionID, "restart", err)
		return
	}

	// The whole transcript went back with the state, so there is nothing to
	// append: the screen is redrawn from what is now stored.
	s.redrawGame(w, r, sessionID)
}

// deleteGame throws a game away, once the player has confirmed.
//
// It is a POST for the reason deleting a save is: a form is all a browser
// without JavaScript can send. The browser goes to the lobby rather than back
// to the game, because the page it was on no longer describes anything.
func (s *Server) deleteGame(w http.ResponseWriter, r *http.Request, player user) {
	sessionID := r.PathValue("id")

	if err := s.games.Delete(r.Context(), player.ID, sessionID); err != nil {
		if errors.Is(err, game.ErrSessionNotFound) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}

	to := url.URL{
		Path:     "/",
		RawQuery: url.Values{"notice": {"That game is gone, and the saves it held with it."}}.Encode(),
	}
	s.sendTo(w, r, to.String())
}

// turnFailed reports a turn that did not happen.
//
// Nothing was written on a failed turn, so the stored state is still the good
// one and the player has lost nothing but the command. Each outcome is said
// plainly in the terminal rather than replacing the page with an error
// document.
func (s *Server) turnFailed(w http.ResponseWriter, r *http.Request, sessionID, command string, err error) {
	switch {
	case errors.Is(err, game.ErrSessionNotFound):
		http.NotFound(w, r)
		return

	case errors.Is(err, game.ErrGameOver):
		s.answerTurn(w, r, sessionID, command, "The story has ended.")
		return

	case errors.Is(err, game.ErrVersionConflict):
		// Another tab played first. The command was issued against a screen
		// that is no longer the current one, so it is refused rather than
		// replayed, and the browser is sent to redraw.
		s.answerTurn(w, r, sessionID, command, "This game moved on somewhere else. Reload to catch up.")
		return

	case errors.Is(err, game.ErrStoryUnavailable):
		s.logger.ErrorContext(r.Context(), "a stored game names a story this binary does not carry",
			"session", sessionID, "error", err)
		s.answerTurn(w, r, sessionID, command, "This server cannot run that story.")
		return
	}

	fault := game.Classify(err)
	switch fault {
	case game.FaultCanceled:
		// The client is gone. There is nobody to answer.
		return

	case game.FaultTimeout:
		s.logger.WarnContext(r.Context(), "turn timed out", "session", sessionID)
		s.answerTurn(w, r, sessionID, command, "That took too long. Try it again.")
		return

	case game.FaultInvalidState:
		s.logger.ErrorContext(r.Context(), "stored state was refused",
			"session", sessionID, "error", err)
		s.answerTurn(w, r, sessionID, command, "This game cannot be resumed.")
		return
	}

	// An execution fault or an instruction-limit stop is the story or the
	// engine going wrong, and the program counter in the error is what makes
	// it reportable upstream.
	s.logger.ErrorContext(r.Context(), "turn failed",
		"session", sessionID, "fault", fault.String(), "error", err)
	s.answerTurn(w, r, sessionID, command, "Something went wrong playing that turn. Nothing was lost.")
}

// answerTurn reports the outcome of a turn that produced no story output.
//
// It appends a line to the transcript rather than replacing the page: the
// player is looking at a terminal, and a terminal says what happened on the
// next line, with the prompt back underneath it.
func (s *Server) answerTurn(w http.ResponseWriter, r *http.Request, sessionID, command, message string) {
	if !isHTMX(r) {
		to := url.URL{Path: "/games/" + sessionID, RawQuery: url.Values{"notice": {message}}.Encode()}
		http.Redirect(w, r, to.String(), http.StatusSeeOther)
		return
	}

	s.renderFragment(w, r, http.StatusOK, fragment{"turn", struct{ Command, Output string }{
		Command: command,
		Output:  "[" + message + "]\n\n",
	}})
}

// trimPrompt takes the story's trailing prompt off the transcript.
//
// Zork ends every turn with a bare ">" and no newline after it, leaving the
// cursor beside it. In a browser the command field is that cursor and draws its
// own ">", so the story's is relocated there rather than printed on a line of
// its own above it. The stored transcript keeps what the story wrote; only the
// rendering moves it.
func trimPrompt(text string) string { return strings.TrimSuffix(text, ">") }
