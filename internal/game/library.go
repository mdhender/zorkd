// Package game runs Zork sessions on top of the zmachine execution engine.
package game

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/maloquacious/zmachine"

	"github.com/mdhender/zorkd/games"
)

// StoryKey identifies a story file by SHA-256 over its image.
//
// A session records the key of the story it belongs to, because a saved state
// only restores into a machine built from that exact story. Release and serial
// identify an edition rather than a file, and a story's own checksum is 0 in
// early Version 3 stories that carry none, so neither is strong enough to key
// a session by.
type StoryKey [sha256.Size]byte

// StoryKeyOf returns the key of a story image.
func StoryKeyOf(data []byte) StoryKey { return sha256.Sum256(data) }

// String returns the key as lowercase hex.
func (k StoryKey) String() string { return hex.EncodeToString(k[:]) }

// Entry is one loaded, validated story and the metadata the application needs
// to talk about it.
type Entry struct {
	ID    string
	Title string
	File  string
	Key   StoryKey

	// Story is immutable and safe for concurrent use. Any number of
	// machines may be created from it.
	Story *zmachine.Story
}

// Release returns the release number recorded in the story header.
func (e *Entry) Release() uint16 { return e.Story.Release() }

// Serial returns the six-character serial code from the story header.
func (e *Entry) Serial() string { return e.Story.Serial() }

// Size returns the length in bytes of the story image.
func (e *Entry) Size() int { return e.Story.Size() }

// Library holds every story the server can run.
//
// Loading validates each image in full and is the expensive step, so a Library
// is built once at startup and never written to again. It is then safe for
// concurrent use, as are the stories it holds.
type Library struct {
	byKey map[StoryKey]*Entry
	byID  map[string]*Entry
	order []*Entry
}

// Embedded builds a Library from the story files compiled into the binary.
func Embedded() (*Library, error) {
	return NewLibrary(games.All())
}

// NewLibrary validates each source and returns a Library holding the results.
//
// It fails rather than serving a partial catalog: a story that will not load is
// a deployment defect, and discovering it at startup is the point of loading
// everything up front.
func NewLibrary(sources []games.Story) (*Library, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("library: no stories")
	}

	lib := &Library{
		byKey: make(map[StoryKey]*Entry, len(sources)),
		byID:  make(map[string]*Entry, len(sources)),
		order: make([]*Entry, 0, len(sources)),
	}

	for _, src := range sources {
		if src.ID == "" {
			return nil, fmt.Errorf("library: story %q: empty id", src.File)
		}
		if prior, ok := lib.byID[src.ID]; ok {
			return nil, fmt.Errorf("library: %s: duplicate id, already used by %s", src.ID, prior.File)
		}

		story, err := zmachine.LoadStory(src.Data)
		if err != nil {
			return nil, fmt.Errorf("library: %s: load %s: %w", src.ID, src.File, err)
		}

		key := StoryKeyOf(src.Data)
		if prior, ok := lib.byKey[key]; ok {
			return nil, fmt.Errorf("library: %s: same story image as %s", src.ID, prior.ID)
		}

		entry := &Entry{ID: src.ID, Title: src.Title, File: src.File, Key: key, Story: story}
		lib.byKey[key] = entry
		lib.byID[src.ID] = entry
		lib.order = append(lib.order, entry)
	}

	return lib, nil
}

// ByID returns the story a player selects by name when starting a new game.
func (l *Library) ByID(id string) (*Entry, bool) {
	e, ok := l.byID[id]
	return e, ok
}

// ByKey returns the story a stored session belongs to.
func (l *Library) ByKey(key StoryKey) (*Entry, bool) {
	e, ok := l.byKey[key]
	return e, ok
}

// All returns every entry in the order it was loaded.
//
// The returned slice is fresh on each call and may be reordered by the caller;
// the entries it points at are shared and read-only.
func (l *Library) All() []*Entry {
	out := make([]*Entry, len(l.order))
	copy(out, l.order)
	return out
}

// Len reports how many stories the library holds.
func (l *Library) Len() int { return len(l.order) }
