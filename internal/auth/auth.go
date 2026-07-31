// Package auth holds accounts and the passwords that authenticate them.
//
// It knows nothing about HTTP. Logging in here means "this email and password
// identify this user"; turning that answer into a cookie is
// [github.com/mdhender/zorkd/internal/session]'s job.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
)

// Errors a caller is expected to handle.
var (
	// ErrInvalidCredentials means the email and password did not identify a
	// user. It is deliberately the same answer for an unknown address and a
	// wrong password: telling them apart tells a stranger which addresses have
	// accounts.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrEmailTaken means an account already exists for that address.
	ErrEmailTaken = errors.New("auth: email is already registered")

	// ErrUserNotFound means no account exists under that identifier.
	ErrUserNotFound = errors.New("auth: user not found")

	// ErrInvalidEmail means the address is not one this application will store.
	ErrInvalidEmail = errors.New("auth: invalid email address")

	// ErrWeakPassword means the password is outside the accepted length.
	ErrWeakPassword = errors.New("auth: unacceptable password")
)

// A User is an account. It carries no secret, so it is safe to hand to a
// template, a log line, or a request context.
type User struct {
	ID    string
	Email string
}

// A Record is a stored account: the user together with the hash that
// authenticates them. It stays inside this package and the store beneath it.
type Record struct {
	User
	PasswordHash string
}

// A Store holds accounts durably.
//
// Implementations may be used from several goroutines at once.
type Store interface {
	// CreateUser stores a new account and returns it with an identifier
	// assigned by the store. It returns ErrEmailTaken if the address is
	// already registered.
	CreateUser(ctx context.Context, record Record) (User, error)

	// UserByEmail returns the account registered under the normalized address,
	// or ErrUserNotFound.
	UserByEmail(ctx context.Context, email string) (Record, error)

	// UserByID returns the account, or ErrUserNotFound.
	UserByID(ctx context.Context, id string) (User, error)
}

// A Service registers and authenticates users.
//
// One Service serves every request and is safe for concurrent use.
type Service struct {
	store Store
}

// NewService returns a Service over the store.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("auth: service: nil store")
	}
	return &Service{store: store}, nil
}

// Register creates an account.
//
// The password is hashed here and the plaintext is never handed to the store.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	address, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := checkPasswordLength(password); err != nil {
		return User{}, err
	}

	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	user, err := s.store.CreateUser(ctx, Record{
		User:         User{Email: address},
		PasswordHash: hash,
	})
	if err != nil {
		return User{}, fmt.Errorf("auth: register: %w", err)
	}

	return user, nil
}

// Authenticate returns the user the email and password identify.
//
// An unknown address still costs a password verification. Returning early
// would make a failed login measurably faster for addresses that have no
// account, which is a way of asking the server who its users are.
func (s *Service) Authenticate(ctx context.Context, email, password string) (User, error) {
	address, err := NormalizeEmail(email)
	if err != nil {
		_ = VerifyPassword(decoyHash(), password)
		return User{}, ErrInvalidCredentials
	}

	record, err := s.store.UserByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_ = VerifyPassword(decoyHash(), password)
			return User{}, ErrInvalidCredentials
		}
		return User{}, fmt.Errorf("auth: authenticate: %w", err)
	}

	if err := VerifyPassword(record.PasswordHash, password); err != nil {
		if errors.Is(err, ErrPasswordMismatch) {
			return User{}, ErrInvalidCredentials
		}
		// An unreadable stored hash is an operational problem rather than a
		// wrong guess, and it is worth saying so in the log.
		return User{}, fmt.Errorf("auth: user %s: %w", record.ID, err)
	}

	return record.User, nil
}

// User returns the account under id.
func (s *Service) User(ctx context.Context, id string) (User, error) {
	user, err := s.store.UserByID(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("auth: user %s: %w", id, err)
	}
	return user, nil
}

// MaxEmailLength bounds an address as stored. It is well past the longest
// address anybody has, and it stops a registration form from proposing a
// database row of arbitrary size.
const MaxEmailLength = 254

// NormalizeEmail returns the form of the address used for lookup: trimmed of
// surrounding space and lowercased.
//
// Registration and login must agree on this, or an account becomes reachable
// only by typing the address exactly as it was first entered. Only the address
// itself is accepted — "Zork <zork@example.com>" is a display form, not an
// identity.
func NormalizeEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" || len(trimmed) > MaxEmailLength {
		return "", fmt.Errorf("auth: %q: %w", email, ErrInvalidEmail)
	}

	address, err := mail.ParseAddress(trimmed)
	if err != nil || !strings.EqualFold(address.Address, trimmed) {
		return "", fmt.Errorf("auth: %q: %w", email, ErrInvalidEmail)
	}

	return strings.ToLower(address.Address), nil
}

// decoyHash is a real hash of a password nobody knows, used to spend the time a
// genuine verification would have spent. It is computed once, on the first
// login that needs it.
var decoyHash = sync.OnceValue(func() string {
	hash, err := HashPassword("a password no account has")
	if err != nil {
		// HashPassword fails only if the system random source does, which is
		// not a condition this can paper over.
		panic("auth: cannot hash: " + err.Error())
	}
	return hash
})
