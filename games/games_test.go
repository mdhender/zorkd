package games

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The embedded bytes must be the files on disk. A go:embed pattern naming the
// wrong file still compiles, and the mistake would otherwise surface as saves
// that no longer restore.
func TestEmbeddedMatchesDisk(t *testing.T) {
	for _, story := range All() {
		t.Run(story.ID, func(t *testing.T) {
			path := filepath.Join(story.ID, story.File)

			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if !bytes.Equal(story.Data, onDisk) {
				t.Errorf("embedded %s differs from %s (%d bytes embedded, %d on disk)",
					story.ID, path, len(story.Data), len(onDisk))
			}
		})
	}
}

func TestAllIsWellFormed(t *testing.T) {
	seen := make(map[string]bool)

	for _, story := range All() {
		switch {
		case story.ID == "":
			t.Errorf("story %q has no id", story.File)
		case story.Title == "":
			t.Errorf("%s has no title", story.ID)
		case story.File == "":
			t.Errorf("%s has no file name", story.ID)
		case len(story.Data) == 0:
			t.Errorf("%s embedded no data", story.ID)
		case seen[story.ID]:
			t.Errorf("%s listed twice", story.ID)
		}
		seen[story.ID] = true
	}

	if len(seen) == 0 {
		t.Fatal("All() returned no stories")
	}
}
