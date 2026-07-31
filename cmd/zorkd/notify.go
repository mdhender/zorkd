package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

// The two service-manager notifications this server sends. READY=1 says the
// listener is bound, which is what lets a unit be Type=notify and lets other
// units be ordered after it and mean it. STOPPING=1 says a stop that takes a
// moment is deliberate rather than a unit going quiet.
//
// WATCHDOG=1 is deliberately absent: a watchdog promises that a wedged process
// will be noticed, and keeping that promise needs a keepalive that proves the
// server is serving rather than merely running.
const (
	notifyReady    = "READY=1"
	notifyStopping = "STOPPING=1"
)

// notify sends one state notification to the service manager listening on the
// socket named by $NOTIFY_SOCKET.
//
// An unset or empty NOTIFY_SOCKET means nothing is listening — not under
// systemd, or a unit declared Type=exec — so there is nothing to report and no
// error to return. No build tags guard this: the variable is simply absent
// everywhere systemd is.
//
// A leading '@' in the socket name means the Linux abstract namespace, which
// net.Dial understands as it stands; it is passed through rather than
// translated.
//
// The whole protocol used here is one datagram carrying the state as written,
// so the bytes are sent exactly as given. systemd parses them.
func notify(state string) error {
	socket, ok := os.LookupEnv("NOTIFY_SOCKET")
	if !ok || socket == "" {
		return nil
	}

	conn, err := net.Dial("unixgram", socket)
	if err != nil {
		return fmt.Errorf("notify %q on %s: %w", state, socket, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("notify %q on %s: %w", state, socket, err)
	}
	return nil
}

// notifyServiceManager sends a notification and logs a failure rather than
// treating it as fatal.
//
// A server that works but failed to announce itself is more useful than one
// that refuses to start over it, and systemd ends the unit on its own
// TimeoutStartSec anyway — with this log line already saying why.
func notifyServiceManager(logger *slog.Logger, state string) {
	if err := notify(state); err != nil {
		logger.Error("notifying the service manager failed", "state", state, "error", err)
	}
}
