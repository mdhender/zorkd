// Command zorkplay plays one of the embedded Zork stories from a terminal.
//
// It exists to separate engine and state debugging from web debugging. It
// drives the same turn cycle the server will: the machine is rebuilt from the
// story and the previous turn's state on every command, so a bug that only
// appears when a machine is thrown away appears here too.
//
// The state lives in memory for the length of the session. Durable persistence
// is the server's job, not this program's.
//
//	zorkplay [flags] [game]
//
// The game defaults to zork1. Run with -list to see the others.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/maloquacious/zmachine"

	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/terminal"
)

const defaultGame = "zork1"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "zorkplay: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("zorkplay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: zorkplay [flags] [game]\n\nplays an embedded Zork story; game defaults to %s\n\nflags:\n", defaultGame)
		fs.PrintDefaults()
	}

	var (
		list    = fs.Bool("list", false, "list the games this binary carries and exit")
		seed    = fs.Uint64("seed", 0, "fixed random seed, for a reproducible session")
		timeout = fs.Duration("timeout", game.DefaultTurnTimeout, "wall-clock bound on one turn")
		limit   = fs.Uint64("limit", game.DefaultInstructionLimit, "instruction bound on one turn")
		width   = fs.Int("width", terminal.DefaultWidth, "column count to wrap the story text to; 0 leaves it unwrapped")
		verbose = fs.Bool("v", false, "log engine diagnostics to stderr")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("expected at most one game, got %d", fs.NArg())
	}

	library, err := game.Embedded()
	if err != nil {
		return fmt.Errorf("load games: %w", err)
	}

	if *list {
		return listGames(stdout, library)
	}

	id := defaultGame
	if fs.NArg() == 1 {
		id = fs.Arg(0)
	}
	entry, ok := library.ByID(id)
	if !ok {
		return fmt.Errorf("unknown game %q; run with -list", id)
	}

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}

	opts := []game.RunnerOption{
		game.WithLogger(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))),
		game.WithTurnTimeout(*timeout),
		game.WithInstructionLimit(*limit),
	}
	// A seed of zero is a legitimate seed, so honor the flag only when it was
	// actually given; otherwise the generator is seeded unpredictably, which is
	// what a real game wants.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "seed" {
			opts = append(opts, game.WithRandomSeed(*seed))
		}
	})

	return play(context.Background(), game.NewRunner(opts...), entry, *width, stdin, stdout, stderr)
}

func listGames(w io.Writer, library *game.Library) error {
	for _, entry := range library.All() {
		if _, err := fmt.Fprintf(w, "%-6s %-38s release %d / serial %s\n",
			entry.ID, entry.Title, entry.Release(), entry.Serial()); err != nil {
			return err
		}
	}
	return nil
}

// play runs a session to its end: the story halts, the reader reaches end of
// input, or a turn fails in a way that is not worth continuing from.
func play(ctx context.Context, runner *game.Runner, entry *game.Entry, width int, stdin io.Reader, stdout, stderr io.Writer) error {
	result, err := runner.Start(ctx, entry)
	if err != nil {
		return fmt.Errorf("start %s: %w", entry.ID, err)
	}
	fmt.Fprint(stdout, terminal.NewTurn("", result).Text(width))

	state := result.State
	input := bufio.NewScanner(stdin)

	for result.Status != zmachine.Halted {
		if !input.Scan() {
			fmt.Fprintln(stdout)
			return input.Err()
		}
		command := input.Text()

		// The same bound the server applies, applied before the engine sees
		// the line rather than after it has been silently truncated.
		if len(command) > game.MaxCommandBytes {
			fmt.Fprintf(stderr, "\nzorkplay: command is %d bytes, limit is %d\n", len(command), game.MaxCommandBytes)
			reprompt(stdout)
			continue
		}

		result, err = runner.Run(ctx, entry, state, command)
		if err != nil {
			fault := game.Classify(err)
			fmt.Fprintf(stderr, "\nzorkplay: %v\n", err)

			// A failed turn did not happen, so the state from the previous one
			// is still the right one to play the next command against.
			if fault.Retryable() {
				fmt.Fprintln(stderr, "the turn did not happen; try it again")
				reprompt(stdout)
				continue
			}
			return fmt.Errorf("%s turn: %w", fault, err)
		}

		state = result.State
		fmt.Fprint(stdout, terminal.NewTurn(command, result).Text(width))
	}

	return nil
}

// reprompt redraws the story's prompt after this program has written something
// of its own where the player's answer would have gone.
func reprompt(w io.Writer) { fmt.Fprint(w, "\n>") }
