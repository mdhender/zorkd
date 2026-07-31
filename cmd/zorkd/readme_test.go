package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

// readmePath is relative because a test runs with its own package directory as
// the working directory.
const readmePath = "../../README.md"

var (
	// PrintDefaults writes one "  -name" line per flag and indents the usage
	// text beneath it, so nothing else in the output can match this.
	printedFlag = regexp.MustCompile(`^  -([^ \t=]+)`)

	// A row of a flag table names its flag in the first cell, as code.
	documentedFlag = regexp.MustCompile("^\\| *`-([^`]+)`")
)

// TestREADMEDocumentsEveryFlag holds the README's flag tables to what cmd/zorkd
// actually accepts, in both directions: a flag added later cannot go
// undocumented, and a row cannot outlive the flag it describes. The project has
// no CI, so a test is the only thing that will ever notice either one.
//
// The flag sets are locals inside serve and runInvite, which is where they
// belong — nothing else needs them — so this reads them back the way an
// operator would, by asking for the usage that fs.Usage prints from
// fs.PrintDefaults. That costs a small parse and needs no restructuring, and it
// covers the invite subcommand on the same terms as the serving flags.
func TestREADMEDocumentsEveryFlag(t *testing.T) {
	// serve reads these before it parses anything, and reports one it cannot
	// read rather than falling back. An empty value is read as unset, so this
	// runs the same whatever the machine has exported.
	for _, name := range []string{"ZORK_TURN_TIMEOUT", "ZORK_INSTRUCTION_LIMIT"} {
		t.Setenv(name, "")
	}

	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}

	tests := []struct {
		name    string
		command string
		heading string
		usage   func(stderr io.Writer) error
	}{
		{
			name:    "serve",
			command: "zorkd",
			heading: "### Server flags",
			usage:   func(stderr io.Writer) error { return run([]string{"-h"}, stderr) },
		},
		{
			name:    "invite",
			command: "zorkd invite",
			heading: "### Issuing an invitation",
			usage:   func(stderr io.Writer) error { return runInvite([]string{"-h"}, io.Discard, stderr) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defined := definedFlags(t, tt.usage)
			documented := documentedFlags(t, string(readme), tt.heading)

			for name := range defined {
				if !documented[name] {
					t.Errorf("%s defines -%s and README.md does not document it: add a row to the table under %q", tt.command, name, tt.heading)
				}
			}
			for name := range documented {
				if !defined[name] {
					t.Errorf("the table under %q in README.md documents -%s, which %s does not define: drop the row", tt.heading, name, tt.command)
				}
			}
		})
	}
}

// definedFlags reports the flags a command defines, read out of the usage it
// prints for -h.
func definedFlags(t *testing.T, usage func(stderr io.Writer) error) map[string]bool {
	t.Helper()

	var stderr bytes.Buffer
	if err := usage(&stderr); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h returned %v, want %v", err, flag.ErrHelp)
	}
	printed := stderr.String()

	flags := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(printed))
	for scanner.Scan() {
		if match := printedFlag.FindStringSubmatch(scanner.Text()); match != nil {
			flags[match[1]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	// A parse that finds nothing would agree with any README at all.
	if len(flags) == 0 {
		t.Fatalf("no flags found in usage output:\n%s", printed)
	}
	return flags
}

// documentedFlags reports the flags named by the first column of the table
// under a heading, up to the next heading.
func documentedFlags(t *testing.T, readme, heading string) map[string]bool {
	t.Helper()

	lines := strings.Split(readme, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("README.md has no %q heading: the flag table was moved or renamed, and this test cannot find it", heading)
	}

	flags := make(map[string]bool)
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "#") {
			break
		}
		if match := documentedFlag.FindStringSubmatch(line); match != nil {
			flags[match[1]] = true
		}
	}
	if len(flags) == 0 {
		t.Fatalf("no flag rows under the %q heading in README.md", heading)
	}
	return flags
}
