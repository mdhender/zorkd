package migrations

import (
	"strings"
	"testing"
)

func TestAllIsOrdered(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no migrations embedded")
	}

	for i, migration := range all {
		if migration.Version < 1 {
			t.Errorf("%s: version %d is not positive", migration.Name, migration.Version)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			t.Errorf("%s: empty", migration.Name)
		}
		if !strings.HasSuffix(migration.Name, ".sql") {
			t.Errorf("%s: not a .sql file", migration.Name)
		}
		if i > 0 && all[i-1].Version >= migration.Version {
			t.Errorf("%s comes after %s but its version does not",
				migration.Name, all[i-1].Name)
		}
	}
}

func TestVersionOf(t *testing.T) {
	tests := []struct {
		name string
		file string
		want int
		bad  bool
	}{
		{name: "first", file: "0001_games.sql", want: 1},
		{name: "later", file: "0042_saves.sql", want: 42},
		{name: "no description", file: "0001.sql", bad: true},
		{name: "no version", file: "games.sql", bad: true},
		{name: "zero", file: "0000_start.sql", bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := versionOf(tt.file)
			if tt.bad {
				if err == nil {
					t.Fatalf("versionOf(%q) = %d, want an error", tt.file, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionOf(%q) error = %v", tt.file, err)
			}
			if got != tt.want {
				t.Errorf("versionOf(%q) = %d, want %d", tt.file, got, tt.want)
			}
		})
	}
}
