package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	const password = "a good long password"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if strings.Contains(hash, password) {
		t.Fatalf("the password is in its own hash: %q", hash)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash = %q, want an argon2id PHC string", hash)
	}

	if err := VerifyPassword(hash, password); err != nil {
		t.Errorf("VerifyPassword() with the right password error = %v", err)
	}
	if err := VerifyPassword(hash, "the wrong password"); !errors.Is(err, ErrPasswordMismatch) {
		t.Errorf("VerifyPassword() error = %v, want %v", err, ErrPasswordMismatch)
	}
}

// Two hashes of the same password differ, because the salt does. Equal hashes
// would tell anyone reading the table which accounts share a password.
func TestHashIsSalted(t *testing.T) {
	first, err := HashPassword("a good long password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("a good long password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if first == second {
		t.Fatal("two hashes of the same password are identical")
	}
	if err := VerifyPassword(second, "a good long password"); err != nil {
		t.Errorf("VerifyPassword() error = %v", err)
	}
}

func TestHashRejectsUnacceptablePasswords(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{"empty", ""},
		{"too short", "shortpw"},
		{"too long", strings.Repeat("x", MaxPasswordLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := HashPassword(tt.password); !errors.Is(err, ErrWeakPassword) {
				t.Errorf("HashPassword() error = %v, want %v", err, ErrWeakPassword)
			}
		})
	}

	if _, err := HashPassword(strings.Repeat("x", MaxPasswordLength)); err != nil {
		t.Errorf("HashPassword() at the limit error = %v", err)
	}
}

// A stored hash has been out of this program's hands. Everything unexpected is
// refused rather than defaulted, and nothing verifies by accident.
func TestVerifyRejectsMalformedHashes(t *testing.T) {
	good, err := HashPassword("a good long password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	fields := strings.Split(good, "$")

	tests := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"not a phc string", "hunter2"},
		{"too few fields", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA"},
		{"wrong algorithm", "$argon2i$" + strings.Join(fields[2:], "$")},
		{"bcrypt", "$2y$10$abcdefghijklmnopqrstuv"},
		{"unknown version", "$argon2id$v=16$" + strings.Join(fields[3:], "$")},
		{"unreadable version", "$argon2id$v=x$" + strings.Join(fields[3:], "$")},
		{"unreadable parameters", "$argon2id$v=19$memory=lots$" + strings.Join(fields[4:], "$")},
		{"zero parameters", "$argon2id$v=19$m=0,t=0,p=0$" + strings.Join(fields[4:], "$")},
		{"unreadable salt", strings.Join(fields[:4], "$") + "$not base64!$" + fields[5]},
		{"no salt", strings.Join(fields[:4], "$") + "$$" + fields[5]},
		{"unreadable key", strings.Join(fields[:5], "$") + "$not base64!"},
		{"no key", strings.Join(fields[:5], "$") + "$"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyPassword(tt.encoded, "a good long password")
			if err == nil {
				t.Fatal("VerifyPassword() = nil error, want failure")
			}
			// A hash this build cannot read is an operational problem, not a
			// wrong guess, and the two must not be reported as the same thing.
			if errors.Is(err, ErrPasswordMismatch) {
				t.Errorf("VerifyPassword() error = %v, want a malformed-hash error", err)
			}
		})
	}
}

// A hash written under different cost parameters still verifies, because the
// parameters travel inside it. Raising them must not lock everybody out.
func TestVerifyHonorsTheStoredParameters(t *testing.T) {
	weaker := hashParams{memory: 8 * 1024, time: 1, threads: 1, saltLen: 16, keyLen: 32}

	hash, err := hashWith("a good long password", weaker)
	if err != nil {
		t.Fatalf("hashWith() error = %v", err)
	}
	if !strings.Contains(hash, "m=8192,t=1,p=1") {
		t.Fatalf("hash = %q, want the weaker parameters encoded in it", hash)
	}

	if err := VerifyPassword(hash, "a good long password"); err != nil {
		t.Errorf("VerifyPassword() error = %v", err)
	}
}
