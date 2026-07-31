// Package migrations holds the versioned SQL that builds the database schema.
//
// Files are named NNNN_description.sql and are applied in order, once each. A
// migration that has been applied is never edited: the next change is a new
// file, because a released schema exists in databases this process does not
// own.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// A Migration is one schema step.
type Migration struct {
	// Version is the number the file name begins with. It is written to the
	// database's user_version once the migration has been applied.
	Version int

	// Name is the file it came from, for error messages.
	Name string

	// SQL is the whole file.
	SQL string
}

// All returns every migration in the order it must be applied.
func All() ([]Migration, error) {
	entries, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrations: list: %w", err)
	}
	sort.Strings(entries)

	out := make([]Migration, 0, len(entries))
	for _, name := range entries {
		version, err := versionOf(name)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 && out[len(out)-1].Version >= version {
			return nil, fmt.Errorf("migrations: %s: version %d is not after %s",
				name, version, out[len(out)-1].Name)
		}

		sql, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("migrations: %s: read: %w", name, err)
		}

		out = append(out, Migration{Version: version, Name: name, SQL: string(sql)})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("migrations: none embedded")
	}
	return out, nil
}

func versionOf(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migrations: %s: expected NNNN_description.sql", name)
	}

	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migrations: %s: version: %w", name, err)
	}
	if version < 1 {
		return 0, fmt.Errorf("migrations: %s: version %d is not positive", name, version)
	}

	return version, nil
}
