package httpserver

import (
	"errors"
	"net/http"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/session"
)

// credentialsView is what the login and registration pages render.
//
// The email is echoed back so a failed attempt does not make the player type it
// again. The password never is.
type credentialsView struct {
	Email       string
	Error       string
	MinPassword int
}

func (s *Server) showLogin(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.UserID(r.Context(), r); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, "login.html", http.StatusOK, credentialsView{})
}

func (s *Server) logIn(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.renderPage(w, r, "login.html", http.StatusBadRequest,
			credentialsView{Error: "That request could not be read."})
		return
	}

	email := r.PostFormValue("email")

	account, err := s.accounts.Authenticate(r.Context(), email, r.PostFormValue("password"))
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			// A store that is down, or a hash this build cannot read. Worth
			// saying so in the log; the player is told the same thing either
			// way.
			s.logger.ErrorContext(r.Context(), "authentication failed", "error", err)
		}
		s.renderPage(w, r, "login.html", http.StatusUnauthorized, credentialsView{
			Email: email,
			Error: "That email and password do not match an account.",
		})
		return
	}

	if err := s.sessions.Start(r.Context(), w, account.ID); err != nil {
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) showRegister(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.UserID(r.Context(), r); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, "register.html", http.StatusOK,
		credentialsView{MinPassword: auth.MinPasswordLength})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	view := credentialsView{MinPassword: auth.MinPasswordLength}

	if err := parseForm(w, r); err != nil {
		view.Error = "That request could not be read."
		s.renderPage(w, r, "register.html", http.StatusBadRequest, view)
		return
	}

	view.Email = r.PostFormValue("email")

	account, err := s.accounts.Register(r.Context(), view.Email, r.PostFormValue("password"))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrEmailTaken):
			view.Error = "There is already an account for that address."
		case errors.Is(err, auth.ErrInvalidEmail):
			view.Error = "That does not look like an email address."
		case errors.Is(err, auth.ErrWeakPassword):
			view.Error = "That password is not long enough."
		default:
			s.fail(w, r, err)
			return
		}
		s.renderPage(w, r, "register.html", http.StatusBadRequest, view)
		return
	}

	if err := s.sessions.Start(r.Context(), w, account.ID); err != nil {
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logOut ends the session on the server as well as clearing the cookie, so a
// copied cookie stops working.
func (s *Server) logOut(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.End(r.Context(), w, r); err != nil && !errors.Is(err, session.ErrNoSession) {
		s.logger.ErrorContext(r.Context(), "ending the session failed", "error", err)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
