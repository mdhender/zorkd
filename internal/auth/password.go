package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password length bounds.
//
// The minimum is a floor, not a policy: length is the only property of a
// password worth insisting on, and composition rules push people toward worse
// ones. The maximum keeps a request from asking the server to hash a megabyte.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 1024
)

// ErrPasswordMismatch means the password does not match the hash.
var ErrPasswordMismatch = errors.New("auth: password does not match")

// hashParams are the Argon2id cost parameters.
//
// These are OWASP's current recommendation for Argon2id: 19 MiB of memory, two
// passes, one lane. They are encoded into every hash, so raising them later
// does not invalidate the hashes written under the old ones.
type hashParams struct {
	memory  uint32 // KiB
	time    uint32 // passes
	threads uint8  // lanes
	saltLen uint32
	keyLen  uint32
}

var defaultParams = hashParams{
	memory:  19 * 1024,
	time:    2,
	threads: 1,
	saltLen: 16,
	keyLen:  32,
}

// HashPassword returns a PHC-encoded Argon2id hash of the password.
//
// The salt and the cost parameters are part of the returned string, so
// verifying needs nothing but the string and the password.
func HashPassword(password string) (string, error) {
	if err := checkPasswordLength(password); err != nil {
		return "", err
	}
	return hashWith(password, defaultParams)
}

func hashWith(password string, p hashParams) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether the password matches the encoded hash.
//
// It returns ErrPasswordMismatch for a password that does not match and a
// different error for a hash this build cannot read. Both are failures to
// authenticate; the distinction is for the log, because an unreadable hash is
// an operational problem rather than a wrong guess.
func VerifyPassword(encoded, password string) error {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// b64 is the unpadded base64 the PHC string format uses.
var b64 = base64.RawStdEncoding

// decodeHash reads a PHC-encoded Argon2id string.
//
// It is parsing data that has been out of this program's hands, so every field
// is checked and anything unexpected is refused rather than defaulted.
func decodeHash(encoded string) (hashParams, []byte, []byte, error) {
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 || fields[0] != "" {
		return hashParams{}, nil, nil, fmt.Errorf("auth: malformed password hash")
	}
	if fields[1] != "argon2id" {
		return hashParams{}, nil, nil, fmt.Errorf("auth: password hash algorithm %q is not argon2id", fields[1])
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil {
		return hashParams{}, nil, nil, fmt.Errorf("auth: malformed password hash version")
	}
	if version != argon2.Version {
		return hashParams{}, nil, nil, fmt.Errorf("auth: password hash version %d, this build writes %d",
			version, argon2.Version)
	}

	var p hashParams
	if _, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return hashParams{}, nil, nil, fmt.Errorf("auth: malformed password hash parameters")
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return hashParams{}, nil, nil, fmt.Errorf("auth: password hash parameters are out of range")
	}

	salt, err := b64.DecodeString(fields[4])
	if err != nil || len(salt) == 0 {
		return hashParams{}, nil, nil, fmt.Errorf("auth: malformed password hash salt")
	}
	key, err := b64.DecodeString(fields[5])
	if err != nil || len(key) == 0 {
		return hashParams{}, nil, nil, fmt.Errorf("auth: malformed password hash key")
	}

	p.saltLen = uint32(len(salt))
	p.keyLen = uint32(len(key))

	return p, salt, key, nil
}

func checkPasswordLength(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return fmt.Errorf("auth: password is shorter than %d characters: %w", MinPasswordLength, ErrWeakPassword)
	case len(password) > MaxPasswordLength:
		return fmt.Errorf("auth: password is longer than %d characters: %w", MaxPasswordLength, ErrWeakPassword)
	}
	return nil
}
