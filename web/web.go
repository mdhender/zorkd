// Package web carries the server's templates and static assets.
//
// They are embedded for the same reason the story files are: deployment is the
// binary and its database, and a server cannot come up missing the page it was
// meant to serve.
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.html
var templates embed.FS

//go:embed static
var static embed.FS

// Templates returns the HTML templates, rooted at the template names.
func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		// The directory is embedded above; its absence is a build problem.
		panic("web: templates: " + err.Error())
	}
	return sub
}

// Static returns the files served under /static/.
func Static() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic("web: static: " + err.Error())
	}
	return sub
}
