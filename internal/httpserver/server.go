// Package httpserver serves the web terminal.
//
// It owns transport and presentation and nothing else: it does not import the
// engine, and it holds no Z-machine semantics. A turn is played by
// [game.Service]; this package decides what a request means and what the
// response looks like.
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/session"
	"github.com/mdhender/zorkd/web"
)

// MaxFormBytes bounds a request body.
//
// The largest thing a player posts is one line of a command, so this is
// generous. It exists so that a client cannot make the server read for as long
// as it feels like sending.
const MaxFormBytes = 16 << 10

// A Server routes requests to the services beneath it.
type Server struct {
	games    *game.Service
	accounts *auth.Service
	sessions *session.Manager
	library  *game.Library
	logger   *slog.Logger

	templates *templates
	static    http.Handler

	// The unauthenticated routes are limited; everything else is behind a
	// session, which is a bound of its own.
	logins        *attemptLimit
	registrations *attemptLimit
}

// New returns a Server. Every dependency is required except the logger, which
// defaults to discarding.
func New(games *game.Service, accounts *auth.Service, sessions *session.Manager, library *game.Library, logger *slog.Logger) (*Server, error) {
	switch {
	case games == nil:
		return nil, errors.New("httpserver: nil game service")
	case accounts == nil:
		return nil, errors.New("httpserver: nil auth service")
	case sessions == nil:
		return nil, errors.New("httpserver: nil session manager")
	case library == nil:
		return nil, errors.New("httpserver: nil library")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	parsed, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	return &Server{
		games:     games,
		accounts:  accounts,
		sessions:  sessions,
		library:   library,
		logger:    logger,
		templates: parsed,
		static:    http.StripPrefix("/static/", http.FileServerFS(web.Static())),
		logins: &attemptLimit{
			source: newLimiter(loginBurst, loginRefill, maxTrackedKeys),
			email:  newLimiter(loginEmailBurst, loginEmailRefill, maxTrackedKeys),
		},
		registrations: &attemptLimit{
			source: newLimiter(registerBurst, registerRefill, maxTrackedKeys),
			email:  newLimiter(registerEmailBurst, registerEmailRefill, maxTrackedKeys),
		},
	}, nil
}

// Handler returns the routes, wrapped in the protections every request needs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", s.static)

	mux.HandleFunc("GET /login", s.showLogin)
	mux.HandleFunc("POST /login", s.logIn)
	mux.HandleFunc("GET /register", s.showRegister)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("POST /logout", s.logOut)

	mux.Handle("GET /{$}", s.requireUser(s.showLobby))
	mux.Handle("POST /games", s.requireUser(s.newGame))
	mux.Handle("GET /games/{id}", s.requireUser(s.showGame))
	mux.Handle("POST /games/{id}/input", s.requireUser(s.playTurn))
	// Restarting is a POST for the same reason deletion is, and for the same
	// reason it is not simply a turn: the confirmation the player answers is a
	// form, and what it throws away is this application's screen as much as the
	// engine's state.
	mux.Handle("POST /games/{id}/restart", s.requireUser(s.restartGame))

	mux.Handle("GET /games/{id}/saves", s.requireUser(s.showSaves))
	mux.Handle("POST /games/{id}/saves", s.requireUser(s.createSave))
	mux.Handle("POST /games/{id}/saves/{save}/restore", s.requireUser(s.restoreSave))
	// Deletion is a POST rather than a DELETE because a form is the only thing
	// a browser with no JavaScript can send, and it can send neither.
	mux.Handle("POST /games/{id}/saves/{save}/delete", s.requireUser(s.deleteSave))

	// Cross-origin protection rejects state-changing requests that a browser
	// says came from somewhere else. It replaces a token in every form, which
	// the HTMX design would otherwise have to carry by hand.
	csrf := http.NewCrossOriginProtection()

	return s.logRequests(csrf.Handler(mux))
}

// user is the authenticated player, carried from the middleware that
// established it to the handler that needs it.
type user struct {
	ID    string
	Email string
}

// requireUser refuses a request that carries no session.
//
// The handler beneath it can then take the user for granted, which is what
// makes it impossible to forget: there is no path to a game handler that has
// not been through here.
func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, user)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := s.sessions.UserID(r.Context(), r)
		if err != nil {
			if !errors.Is(err, session.ErrNoSession) {
				s.logger.ErrorContext(r.Context(), "reading the session failed", "error", err)
			}
			s.redirectToLogin(w, r)
			return
		}

		account, err := s.accounts.User(r.Context(), userID)
		if err != nil {
			// The session outlived the account it named.
			s.logger.WarnContext(r.Context(), "session names a missing account", "error", err)
			_ = s.sessions.End(r.Context(), w, r)
			s.redirectToLogin(w, r)
			return
		}

		h(w, r, user{ID: account.ID, Email: account.Email})
	})
}

// redirectToLogin sends the browser to the login page.
//
// An HTMX request is told to navigate rather than being handed a login page to
// splice into the middle of a terminal.
func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// isHTMX reports whether the request came from htmx rather than from ordinary
// navigation. Fragments go to the first; whole documents go to the second.
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// parseForm reads a bounded form body.
func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
	return r.ParseForm()
}

// logRequests records what happened, without recording what was played.
//
// Player commands are deliberately absent: they are arbitrary text a person
// typed, and a log is not the place for them.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		s.logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"route", r.URL.Path,
			"status", recorder.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	if !rec.written {
		rec.status = status
		rec.written = true
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	rec.written = true
	return rec.ResponseWriter.Write(b)
}
