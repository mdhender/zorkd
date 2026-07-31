package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/invite"
	"github.com/mdhender/zorkd/internal/session"
)

// credentialsView is what the login and registration pages render.
//
// The email is echoed back so a failed attempt does not make the player type it
// again. The password never is.
//
// Invite and Invited are the registration half. Registration is by invitation,
// so the page is either a form carrying the token it arrived with or a refusal
// with no form on it at all: "supply a token to submit" means the form is not
// reachable without one.
type credentialsView struct {
	Email       string
	Error       string
	MinPassword int

	Invite  string
	Invited bool
}

// notInvited is the one answer every unusable invitation gets, whether it is
// unknown, expired, already redeemed, or issued for a different address.
//
// Telling them apart gives an attacker nothing, because the token is not
// something anyone finds by probing — but it gives a legitimate holder of a
// spent link nothing either, so it is decided here rather than by whichever
// check happened to fail first.
const notInvited = "That invitation cannot be used. It may have expired or already been used, or it may have been issued for a different address."

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

	// Refused before Authenticate, which is where the cost is: it verifies a
	// decoy for an address it has never seen so that a wrong address and a wrong
	// password take the same time. The refusal says only that there have been
	// too many attempts — a refusal that read differently for an address with an
	// account would be exactly the answer the decoy exists to withhold.
	if retry, ok := s.logins.allow(r, email); !ok {
		s.tooManyAttempts(w, r, "login.html", retry, credentialsView{Email: email})
		return
	}

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

// showRegister draws the form only for an invitation that can still be
// redeemed, and a refusal for anything else.
//
// The check here is a courtesy so that nobody fills in a form that was never
// going to be accepted. It is not the gate: register checks again, and does not
// trust that this ran.
func (s *Server) showRegister(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.UserID(r.Context(), r); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	token := r.URL.Query().Get("invite")

	invitation, err := s.invitations.Invited(r.Context(), token)
	if err != nil {
		if !errors.Is(err, invite.ErrNotInvited) {
			s.logger.ErrorContext(r.Context(), "reading an invitation failed", "error", err)
		}
		s.renderPage(w, r, "register.html", http.StatusForbidden,
			credentialsView{MinPassword: auth.MinPasswordLength, Error: notInvited})
		return
	}

	s.renderPage(w, r, "register.html", http.StatusOK, credentialsView{
		Email:       invitation.Email,
		MinPassword: auth.MinPasswordLength,
		Invite:      token,
		Invited:     true,
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	view := credentialsView{MinPassword: auth.MinPasswordLength}

	if err := parseForm(w, r); err != nil {
		view.Error = "That request could not be read."
		s.renderPage(w, r, "register.html", http.StatusBadRequest, view)
		return
	}

	view.Email = r.PostFormValue("email")
	view.Invite = r.PostFormValue("invite")
	view.Invited = true

	// Registration cannot hide that an address is taken — the form has to say
	// so — but a stranger walking a list runs out of attempts long before the
	// answers add up to anything, and bulk account creation runs out with them.
	//
	// There is much less behind this than there was: Redeem refuses a request
	// carrying no usable invitation before it hashes anything, so the Argon2id
	// cost on this route is no longer reachable by an unauthenticated stranger.
	// The limit stays because the route is still open to one.
	if retry, ok := s.registrations.allow(r, view.Email); !ok {
		s.tooManyAttempts(w, r, "register.html", retry, view)
		return
	}

	account, err := s.invitations.Redeem(r.Context(), view.Invite, view.Email, r.PostFormValue("password"))
	if err != nil {
		switch {
		case errors.Is(err, invite.ErrNotInvited):
			// The form goes with the invitation: there is nothing here to
			// resubmit, so the refusal is drawn without one.
			s.renderPage(w, r, "register.html", http.StatusForbidden,
				credentialsView{MinPassword: auth.MinPasswordLength, Error: notInvited})
			return
		case errors.Is(err, auth.ErrEmailTaken):
			view.Error = "There is already an account for that address."
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

// tooManyAttempts refuses a rate-limited attempt.
//
// It renders the route's own form with an error on it, the way every other
// refusal on these two routes is rendered, so a player who has simply been too
// quick gets the page back rather than a bare error document with nowhere to
// type.
func (s *Server) tooManyAttempts(w http.ResponseWriter, r *http.Request, page string, retry time.Duration, view credentialsView) {
	view.Error = "Too many attempts. Please wait a moment and try again."

	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
	s.renderPage(w, r, page, http.StatusTooManyRequests, view)
}

// retryAfterSeconds rounds up, and never to zero: Retry-After is whole seconds,
// and rounding down would invite a client back before there is anything to give
// it.
func retryAfterSeconds(retry time.Duration) int {
	seconds := int((retry + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// logOut ends the session on the server as well as clearing the cookie, so a
// copied cookie stops working.
func (s *Server) logOut(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.End(r.Context(), w, r); err != nil && !errors.Is(err, session.ErrNoSession) {
		s.logger.ErrorContext(r.Context(), "ending the session failed", "error", err)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
