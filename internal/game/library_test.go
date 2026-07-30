package game

import (
	"errors"
	"testing"

	"github.com/maloquacious/zmachine"

	"github.com/mdhender/zorkd/games"
)

// The embedded catalog, pinned. A story file is identified by its bytes, so a
// changed hash here means a different file, not a cosmetic edit: every session
// saved against the old one would be refused by a machine built from the new.
var catalog = []struct {
	id      string
	title   string
	file    string
	key     string
	release uint16
	serial  string
	size    int
}{
	{
		id:      "zork1",
		title:   "Zork I: The Great Underground Empire",
		file:    "zork1-r119-880429.z3",
		key:     "37084966477dff679282de42974b2077156b1bd68fad92a65d4ea94d8eb64d79",
		release: 119,
		serial:  "880429",
		size:    86838,
	},
	{
		id:      "zork2",
		title:   "Zork II: The Wizard of Frobozz",
		file:    "zork2-r63-860811.z3",
		key:     "3ae7d5558943e9721f3e4b273c8a7faec1a03a604e1ae4ee1cde472c21cb24ac",
		release: 63,
		serial:  "860811",
		size:    92524,
	},
	{
		id:      "zork3",
		title:   "Zork III: The Dungeon Master",
		file:    "zork3-r25-860811.z3",
		key:     "b637a242865d059890184164ce8dec28554cc80901dcbf26c740b2d1ed0d4eb8",
		release: 25,
		serial:  "860811",
		size:    87984,
	},
}

func TestEmbedded(t *testing.T) {
	lib, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}

	if got, want := lib.Len(), len(catalog); got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}

	for _, want := range catalog {
		t.Run(want.id, func(t *testing.T) {
			entry, ok := lib.ByID(want.id)
			if !ok {
				t.Fatalf("ByID(%q) not found", want.id)
			}
			if entry.Title != want.title {
				t.Errorf("Title = %q, want %q", entry.Title, want.title)
			}
			if entry.File != want.file {
				t.Errorf("File = %q, want %q", entry.File, want.file)
			}
			if got := entry.Key.String(); got != want.key {
				t.Errorf("Key = %s, want %s", got, want.key)
			}
			if got := entry.Release(); got != want.release {
				t.Errorf("Release() = %d, want %d", got, want.release)
			}
			if got := entry.Serial(); got != want.serial {
				t.Errorf("Serial() = %q, want %q", got, want.serial)
			}
			if got := entry.Size(); got != want.size {
				t.Errorf("Size() = %d, want %d", got, want.size)
			}

			byKey, ok := lib.ByKey(entry.Key)
			if !ok {
				t.Fatalf("ByKey(%s) not found", entry.Key)
			}
			if byKey != entry {
				t.Errorf("ByKey and ByID returned different entries")
			}
		})
	}
}

func TestEmbeddedLookupMisses(t *testing.T) {
	lib, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}

	if _, ok := lib.ByID("planetfall"); ok {
		t.Error("ByID(planetfall) found a story the server does not ship")
	}
	if _, ok := lib.ByKey(StoryKey{}); ok {
		t.Error("ByKey(zero) found a story")
	}
}

func TestAllIsACopy(t *testing.T) {
	lib, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}

	first := lib.All()
	first[0] = nil

	second := lib.All()
	if second[0] == nil {
		t.Error("All() handed out the library's own slice")
	}
}

func TestNewLibraryRejects(t *testing.T) {
	valid := games.All()[0]

	tests := []struct {
		name    string
		sources []games.Story
		wantErr error // sentinel to match with errors.Is, or nil to only require an error
	}{
		{
			name:    "no stories",
			sources: nil,
		},
		{
			name:    "empty id",
			sources: []games.Story{{ID: "", File: "zork1.z3", Data: valid.Data}},
		},
		{
			name:    "duplicate id",
			sources: []games.Story{valid, {ID: valid.ID, File: "other.z3", Data: games.All()[1].Data}},
		},
		{
			name:    "same image twice",
			sources: []games.Story{valid, {ID: "zork1-again", File: valid.File, Data: valid.Data}},
		},
		{
			name:    "not a story file",
			sources: []games.Story{{ID: "junk", File: "junk.z3", Data: []byte("this is not a Z-machine story")}},
			wantErr: zmachine.ErrInvalidStory,
		},
		{
			name:    "empty image",
			sources: []games.Story{{ID: "empty", File: "empty.z3", Data: nil}},
			wantErr: zmachine.ErrInvalidStory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib, err := NewLibrary(tt.sources)
			if err == nil {
				t.Fatalf("NewLibrary() = %v, want error", lib)
			}
			if lib != nil {
				t.Errorf("NewLibrary() returned a library alongside its error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("NewLibrary() error = %v, want one wrapping %v", err, tt.wantErr)
			}
		})
	}
}

// A story file that fails to load must fail the whole build rather than leaving
// the server with a catalog it will not discover the hole in until a player
// picks that game.
func TestNewLibraryIsAllOrNothing(t *testing.T) {
	sources := append(games.All(), games.Story{ID: "junk", File: "junk.z3", Data: []byte("nope")})

	if _, err := NewLibrary(sources); err == nil {
		t.Fatal("NewLibrary() = nil error, want failure on the unloadable story")
	}
}
