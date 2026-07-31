package game

import (
	"errors"
	"strings"
	"testing"
)

func TestInterpret(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		intent Intent
		text   string
		save   string
	}{
		{"an ordinary command", "open mailbox", IntentPlay, "open mailbox", ""},
		{"surrounding space", "  look  ", IntentPlay, "look", ""},
		{"nothing at all", "", IntentPlay, "", ""},

		{"a bare save", "save", IntentSave, "save", ""},
		{"a bare restore", "restore", IntentRestore, "restore", ""},
		{"a restart", "restart", IntentRestart, "restart", ""},
		{"case does not matter", "SAVE", IntentSave, "SAVE", ""},
		{"mixed case", "ReStOrE", IntentRestore, "ReStOrE", ""},
		{"a restart in capitals", "RESTART", IntentRestart, "RESTART", ""},
		{"a restart in mixed case", "ReStArT", IntentRestart, "ReStArT", ""},
		{"space around a bare save", "  save  ", IntentSave, "save", ""},
		{"space around a restart", "  restart  ", IntentRestart, "restart", ""},

		// RESTART takes no argument, so nothing is carried off the line.
		{"a restart with words after it", "restart now", IntentRestart, "restart now", ""},

		{"a named save", "save before-troll", IntentSave, "save before-troll", "before-troll"},
		{"a named restore", "restore before-troll", IntentRestore, "restore before-troll", "before-troll"},
		{"a name with spaces", "save two words", IntentSave, "save two words", "two words"},
		{"a tab after the verb", "save\ttroll", IntentSave, "save\ttroll", "troll"},

		// The match is on the whole first word. Anything that merely begins
		// with those letters is the story's.
		{"a word that starts with save", "saves", IntentPlay, "saves", ""},
		{"a word that starts with restore", "restored", IntentPlay, "restored", ""},
		{"a word that starts with restart", "restarts", IntentPlay, "restarts", ""},
		{"the verb somewhere else", "put the save in the box", IntentPlay, "put the save in the box", ""},
		{"restart somewhere else", "tell the wizard to restart", IntentPlay, "tell the wizard to restart", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Interpret(tt.line)

			if got.Intent != tt.intent {
				t.Errorf("Intent = %v, want %v", got.Intent, tt.intent)
			}
			if got.Text != tt.text {
				t.Errorf("Text = %q, want %q", got.Text, tt.text)
			}
			if got.Name != tt.save {
				t.Errorf("Name = %q, want %q", got.Name, tt.save)
			}
		})
	}
}

func TestCleanSaveName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{"an ordinary name", "before-troll", "before-troll", false},
		{"trimmed", "  cellar  ", "cellar", false},
		{"runs of space collapse", "two   words", "two words", false},
		{"a tab is space", "two\twords", "two words", false},
		{"letters beyond ASCII", "café", "café", false},

		{"empty", "", "", true},
		{"only space", "   ", "", true},
		{"a control character", "before\x1b[2Jtroll", "", true},
		{"invalid UTF-8", "troll\xff", "", true},
		{"too long", strings.Repeat("x", MaxSaveNameBytes+1), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanSaveName(tt.in)

			switch {
			case tt.bad && !errors.Is(err, ErrInvalidSaveName):
				t.Errorf("CleanSaveName(%q) error = %v, want %v", tt.in, err, ErrInvalidSaveName)
			case !tt.bad && err != nil:
				t.Errorf("CleanSaveName(%q) error = %v", tt.in, err)
			case !tt.bad && got != tt.want:
				t.Errorf("CleanSaveName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
