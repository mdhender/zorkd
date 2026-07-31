package httpserver

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"

	"github.com/mdhender/zorkd/web"
)

// templates holds one parsed set per page.
//
// Each page is parsed together with the base layout and the shared partials, so
// a page and the fragment a turn returns cannot disagree about what the terminal
// looks like.
type templates struct {
	pages map[string]*template.Template

	// partials renders the fragments a turn returns on their own.
	partials *template.Template
}

// pageNames are the templates that render a whole document.
var pageNames = []string{"login.html", "register.html", "lobby.html", "game.html"}

func parseTemplates() (*templates, error) {
	files := web.Templates()

	parsed := &templates{pages: make(map[string]*template.Template, len(pageNames))}

	for _, name := range pageNames {
		page, err := template.New(name).ParseFS(files, "base.html", "partials.html", name)
		if err != nil {
			return nil, fmt.Errorf("httpserver: parse %s: %w", name, err)
		}
		parsed.pages[name] = page
	}

	partials, err := template.New("partials.html").ParseFS(files, "partials.html")
	if err != nil {
		return nil, fmt.Errorf("httpserver: parse partials: %w", err)
	}
	parsed.partials = partials

	return parsed, nil
}

// renderPage writes a whole document.
//
// It renders into a buffer first: a template that fails half way would
// otherwise have already sent a partial page with a 200 on it, and there would
// be no way left to say that anything went wrong.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name string, status int, data any) {
	page, ok := s.templates.pages[name]
	if !ok {
		s.fail(w, r, fmt.Errorf("httpserver: no page %q", name))
		return
	}

	var buf bytes.Buffer
	if err := page.ExecuteTemplate(&buf, "base", data); err != nil {
		s.fail(w, r, fmt.Errorf("httpserver: render %s: %w", name, err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderFragment writes one named partial, for an HTMX response.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, status int, parts ...fragment) {
	var buf bytes.Buffer

	for _, part := range parts {
		if err := s.templates.partials.ExecuteTemplate(&buf, part.name, part.data); err != nil {
			s.fail(w, r, fmt.Errorf("httpserver: render %s: %w", part.name, err))
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// A fragment is one named partial and the value it renders.
type fragment struct {
	name string
	data any
}

// fail answers a request that could not be served.
//
// The player is told that something went wrong and nothing more; the detail
// goes to the log, where it can name the session without naming it to whoever
// asked.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.ErrorContext(r.Context(), "request failed",
		"method", r.Method,
		"route", r.URL.Path,
		"error", err)

	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}
