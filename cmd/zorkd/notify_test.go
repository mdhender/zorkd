package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readDatagram waits briefly for one datagram and reports it as a string.
func readDatagram(t *testing.T, conn *net.UnixConn) string {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	return string(buf[:n])
}

// listenNotify starts a datagram listener standing in for systemd and points
// NOTIFY_SOCKET at it.
func listenNotify(t *testing.T, name string) *net.UnixConn {
	t.Helper()

	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen on %s: %v", name, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", name)
	return conn
}

// TestNotifyWithoutSocket covers the ordinary case: nothing is listening, so
// there is nothing to report and nothing to fail.
func TestNotifyWithoutSocket(t *testing.T) {
	tests := []struct {
		name  string
		unset bool
		value string
	}{
		{name: "unset", unset: true},
		{name: "empty", value: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unset {
				// t.Setenv restores the previous value on cleanup either way.
				t.Setenv("NOTIFY_SOCKET", "placeholder")
				if err := os.Unsetenv("NOTIFY_SOCKET"); err != nil {
					t.Fatalf("unset NOTIFY_SOCKET: %v", err)
				}
			} else {
				t.Setenv("NOTIFY_SOCKET", tt.value)
			}

			if err := notify(notifyReady); err != nil {
				t.Errorf("notify(%q) = %v, want nil", notifyReady, err)
			}
		})
	}
}

// TestNotifyFilesystemSocket is the deployed shape: systemd hands over a path
// in /run and reads the datagrams sent to it.
func TestNotifyFilesystemSocket(t *testing.T) {
	// The socket path lives in the shortest temporary directory available,
	// because a unix address is bounded at about 100 bytes and t.TempDir()
	// names are long.
	dir, err := os.MkdirTemp("", "zn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	conn := listenNotify(t, filepath.Join(dir, "n.sock"))

	for _, state := range []string{notifyReady, notifyStopping} {
		if err := notify(state); err != nil {
			t.Fatalf("notify(%q) = %v, want nil", state, err)
		}
		// The bytes matter: systemd parses the datagram, so it has to be the
		// state exactly and nothing else — no newline, no padding.
		if got := readDatagram(t, conn); got != state {
			t.Errorf("datagram = %q, want %q", got, state)
		}
	}
}

// TestNotifyAbstractSocket covers the '@' prefix, which names the Linux
// abstract namespace and exists nowhere else.
func TestNotifyAbstractSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the abstract socket namespace is a Linux feature")
	}

	conn := listenNotify(t, fmt.Sprintf("@zorkd-notify-test-%d", os.Getpid()))

	if err := notify(notifyReady); err != nil {
		t.Fatalf("notify(%q) = %v, want nil", notifyReady, err)
	}
	if got := readDatagram(t, conn); got != notifyReady {
		t.Errorf("datagram = %q, want %q", got, notifyReady)
	}
}

// TestNotifyUndialableSocket checks that a socket that is not there is an
// error the caller can log, rather than a panic.
func TestNotifyUndialableSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "zn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "absent.sock")
	t.Setenv("NOTIFY_SOCKET", socket)

	err = notify(notifyReady)
	if err == nil {
		t.Fatal("notify() = nil, want an error")
	}
	for _, want := range []string{notifyReady, socket} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestRunReportsBindFailure checks that a port already in use is reported by
// run itself, naming the address, rather than surfacing later out of the
// serving goroutine.
func TestRunReportsBindFailure(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	defer func() { _ = held.Close() }()

	addr := held.Addr().String()
	args := []string{"-addr", addr, "-database", filepath.Join(t.TempDir(), "zorkd.db")}

	err = run(args, io.Discard)
	if err == nil {
		t.Fatal("run() = nil, want a bind failure")
	}
	want := fmt.Sprintf("listen on %s", addr)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("run() = %q, want it to mention %q", err, want)
	}
	// It must be the bind that failed and not something reported in its place.
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Errorf("run() = %q, want a wrapped *net.OpError", err)
	}
}
