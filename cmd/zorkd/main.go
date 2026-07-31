// Command zorkd serves the Zork trilogy as durable, request-oriented sessions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/database"
	"github.com/mdhender/zorkd/internal/game"
	"github.com/mdhender/zorkd/internal/httpserver"
	"github.com/mdhender/zorkd/internal/session"
)

// HTTP timeouts.
//
// A turn is bounded separately by its own deadline and instruction limit, so
// these only have to be longer than a turn: they exist to stop a client from
// holding a connection open rather than to bound the game.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 10 * time.Second
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "zorkd: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("zorkd", flag.ContinueOnError)
	fs.SetOutput(stderr)

	defaultTimeout, err := envDuration("ZORK_TURN_TIMEOUT", game.DefaultTurnTimeout)
	if err != nil {
		return err
	}
	defaultLimit, err := envUint("ZORK_INSTRUCTION_LIMIT", game.DefaultInstructionLimit)
	if err != nil {
		return err
	}

	var (
		addr     = fs.String("addr", env("ZORK_ADDR", "localhost:8080"), "address to listen on")
		dbPath   = fs.String("database", env("ZORK_DATABASE", "zorkd.db"), "path to the SQLite database")
		timeout  = fs.Duration("turn-timeout", defaultTimeout, "wall-clock bound on one turn")
		limit    = fs.Uint64("instruction-limit", defaultLimit, "instruction bound on one turn")
		insecure = fs.Bool("insecure-cookies", false, "drop the Secure attribute so cookies survive plain HTTP (development only)")
		verbose  = fs.Bool("v", false, "log at debug level")
	)

	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: zorkd [flags]\n\nServes Zork I, II and III over HTTP.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The story files are validated once, here, so a server that comes up is a
	// server that can serve.
	library, err := game.Embedded()
	if err != nil {
		return err
	}

	db, err := database.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("closing the database failed", "error", err)
		}
	}()

	runner := game.NewRunner(
		game.WithLogger(logger),
		game.WithTurnTimeout(*timeout),
		game.WithInstructionLimit(*limit),
	)

	games, err := game.NewService(library, runner, db.Sessions())
	if err != nil {
		return err
	}
	accounts, err := auth.NewService(db.Users())
	if err != nil {
		return err
	}

	var options []session.Option
	if *insecure {
		logger.Warn("session cookies will be sent over plain HTTP")
		options = append(options, session.WithInsecureCookies())
	}
	sessions, err := session.NewManager(db.AuthSessions(), options...)
	if err != nil {
		return err
	}

	if removed, err := sessions.Sweep(ctx); err != nil {
		logger.Warn("sweeping expired sessions failed", "error", err)
	} else if removed > 0 {
		logger.Info("swept expired sessions", "count", removed)
	}

	server, err := httpserver.New(games, accounts, sessions, library, logger)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	failed := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *addr, "stories", library.Len())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
		close(failed)
	}()

	select {
	case err := <-failed:
		if err != nil {
			return fmt.Errorf("listen on %s: %w", *addr, err)
		}
		return nil
	case <-ctx.Done():
	}

	// In-flight turns are given a moment to finish and be stored. A turn cut
	// off here writes nothing, and the state from the previous turn is still
	// the good one.
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}

// env reads a setting from the environment, falling back to a default.
func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}

// envDuration and envUint report a setting that cannot be read rather than
// falling back to the default. A misspelled timeout that silently becomes five
// seconds is worse than a server that refuses to start.
func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", name, value, err)
	}
	return parsed, nil
}

func envUint(name string, fallback uint64) (uint64, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", name, value, err)
	}
	return parsed, nil
}
