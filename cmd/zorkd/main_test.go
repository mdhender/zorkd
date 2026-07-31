package main

import (
	"io"
	"strings"
	"testing"
)

// malformedAddr has no port, so net.SplitHostPort rejects it.
const malformedAddr = "not-an-address"

// nonLoopbackAddrs are the addresses that make -insecure-cookies a refusal.
var nonLoopbackAddrs = []string{
	":8080",
	"0.0.0.0:8080",
	"[::]:8080",
	"192.168.1.10:8080",
	"example.com:8080",
	malformedAddr,
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "default", addr: "localhost:8080", want: true},
		{name: "ipv4 loopback", addr: "127.0.0.1:8080", want: true},
		{name: "ipv4 loopback range", addr: "127.0.0.2:8080", want: true},
		{name: "ipv6 loopback", addr: "[::1]:8080", want: true},
		{name: "wildcard host", addr: ":8080", want: false},
		{name: "ipv4 wildcard", addr: "0.0.0.0:8080", want: false},
		{name: "ipv6 wildcard", addr: "[::]:8080", want: false},
		{name: "private ipv4", addr: "192.168.1.10:8080", want: false},
		{name: "hostname", addr: "example.com:8080", want: false},
		{name: "malformed", addr: malformedAddr, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoopbackAddr(tt.addr); got != tt.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestCheckCookiePolicy(t *testing.T) {
	loopbackAddrs := []string{
		"localhost:8080",
		"127.0.0.1:8080",
		"127.0.0.2:8080",
		"[::1]:8080",
	}

	t.Run("refused", func(t *testing.T) {
		for _, addr := range nonLoopbackAddrs {
			t.Run(addr, func(t *testing.T) {
				err := checkCookiePolicy(true, addr)
				if err == nil {
					t.Fatalf("checkCookiePolicy(true, %q) = nil, want an error", addr)
				}
				// The message has to name both settings and the address, so
				// whoever sees it knows what to change.
				for _, want := range []string{"-insecure-cookies", "-addr", addr} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
			})
		}
	})

	t.Run("accepted with the flag", func(t *testing.T) {
		for _, addr := range loopbackAddrs {
			t.Run(addr, func(t *testing.T) {
				if err := checkCookiePolicy(true, addr); err != nil {
					t.Errorf("checkCookiePolicy(true, %q) = %v, want nil", addr, err)
				}
			})
		}
	})

	// The guard must not make ordinary deployment harder: without the flag,
	// every listener the flag would have refused is still fine.
	t.Run("accepted without the flag", func(t *testing.T) {
		for _, addr := range append(append([]string{}, nonLoopbackAddrs...), loopbackAddrs...) {
			t.Run(addr, func(t *testing.T) {
				if err := checkCookiePolicy(false, addr); err != nil {
					t.Errorf("checkCookiePolicy(false, %q) = %v, want nil", addr, err)
				}
			})
		}
	})
}

// TestRunRefusesInsecureCookiesOnNonLoopback checks that the guard is wired
// into run and returns before anything is opened or served.
func TestRunRefusesInsecureCookiesOnNonLoopback(t *testing.T) {
	args := []string{"-insecure-cookies", "-addr", "0.0.0.0:8080", "-database", t.TempDir() + "/zorkd.db"}

	err := run(args, io.Discard)
	if err == nil {
		t.Fatal("run() = nil, want a refusal")
	}
	want := checkCookiePolicy(true, "0.0.0.0:8080").Error()
	if err.Error() != want {
		t.Fatalf("run() = %q, want %q", err, want)
	}
}
