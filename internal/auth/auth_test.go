package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// memoryStore is enough of a Store to test the Service. The real one is in
// internal/database.
type memoryStore struct {
	mu      sync.Mutex
	byEmail map[string]Record
	byID    map[string]Record
	nextID  int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{byEmail: make(map[string]Record), byID: make(map[string]Record)}
}

func (m *memoryStore) CreateUser(_ context.Context, record Record) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.byEmail[record.Email]; ok {
		return User{}, ErrEmailTaken
	}

	m.nextID++
	record.ID = strconv.FormatInt(m.nextID, 10)
	m.byEmail[record.Email] = record
	m.byID[record.ID] = record

	return record.User, nil
}

func (m *memoryStore) UserByEmail(_ context.Context, email string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.byEmail[email]
	if !ok {
		return Record{}, fmt.Errorf("%q: %w", email, ErrUserNotFound)
	}
	return record, nil
}

func (m *memoryStore) UserByID(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.byID[id]
	if !ok {
		return User{}, fmt.Errorf("%s: %w", id, ErrUserNotFound)
	}
	return record.User, nil
}

func testService(t *testing.T) (*Service, *memoryStore) {
	t.Helper()

	store := newMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, store
}

func TestRegisterAndAuthenticate(t *testing.T) {
	service, store := testService(t)

	user, err := service.Register(t.Context(), "Player@Example.com", "a good long password")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if user.ID == "" {
		t.Fatal("Register() assigned no identifier")
	}
	if user.Email != "player@example.com" {
		t.Errorf("email = %q, want the normalized address", user.Email)
	}

	// The plaintext never reaches the store.
	stored, err := store.UserByEmail(t.Context(), "player@example.com")
	if err != nil {
		t.Fatalf("UserByEmail() error = %v", err)
	}
	if strings.Contains(stored.PasswordHash, "a good long password") {
		t.Fatalf("the password was stored: %q", stored.PasswordHash)
	}

	got, err := service.Authenticate(t.Context(), "  PLAYER@example.com  ", "a good long password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got != user {
		t.Errorf("Authenticate() = %+v, want %+v", got, user)
	}

	found, err := service.User(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("User() error = %v", err)
	}
	if found != user {
		t.Errorf("User() = %+v, want %+v", found, user)
	}
}

// An unknown address and a wrong password must be the same answer. Telling them
// apart tells a stranger which addresses have accounts here.
func TestAuthenticateIsTheSameAnswerEitherWay(t *testing.T) {
	service, _ := testService(t)

	if _, err := service.Register(t.Context(), "player@example.com", "a good long password"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	tests := []struct {
		name     string
		email    string
		password string
	}{
		{"wrong password", "player@example.com", "the wrong password"},
		{"unknown address", "stranger@example.com", "a good long password"},
		{"empty password", "player@example.com", ""},
		{"malformed address", "not an address", "a good long password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.Authenticate(t.Context(), tt.email, tt.password); !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("Authenticate() error = %v, want %v", err, ErrInvalidCredentials)
			}
		})
	}
}

func TestRegisterRefusesADuplicate(t *testing.T) {
	service, _ := testService(t)

	if _, err := service.Register(t.Context(), "player@example.com", "a good long password"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Including under a differently spelled form of the same address.
	if _, err := service.Register(t.Context(), "PLAYER@EXAMPLE.COM", "another good password"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("Register() error = %v, want %v", err, ErrEmailTaken)
	}
}

func TestRegisterValidatesItsInput(t *testing.T) {
	service, _ := testService(t)

	tests := []struct {
		name     string
		email    string
		password string
		want     error
	}{
		{"no address", "", "a good long password", ErrInvalidEmail},
		{"not an address", "player", "a good long password", ErrInvalidEmail},
		{"a display form", "Player <player@example.com>", "a good long password", ErrInvalidEmail},
		{"an address that is too long", strings.Repeat("x", 250) + "@example.com", "a good long password", ErrInvalidEmail},
		{"a short password", "player@example.com", "short", ErrWeakPassword},
		{"no password", "player@example.com", "", ErrWeakPassword},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.Register(t.Context(), tt.email, tt.password); !errors.Is(err, tt.want) {
				t.Errorf("Register() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"player@example.com", "player@example.com", true},
		{"  Player@Example.COM  ", "player@example.com", true},
		{"PLAYER+zork@example.com", "player+zork@example.com", true},
		{"", "", false},
		{"player", "", false},
		{"player@", "", false},
		{"@example.com", "", false},
		{"Player <player@example.com>", "", false},
		{"player@example.com, other@example.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeEmail(tt.in)
			if tt.ok && err != nil {
				t.Fatalf("NormalizeEmail() error = %v", err)
			}
			if !tt.ok {
				if !errors.Is(err, ErrInvalidEmail) {
					t.Fatalf("NormalizeEmail() error = %v, want %v", err, ErrInvalidEmail)
				}
				return
			}
			if got != tt.want {
				t.Errorf("NormalizeEmail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewServiceRequiresAStore(t *testing.T) {
	if _, err := NewService(nil); err == nil {
		t.Fatal("NewService(nil) = nil error, want failure")
	}
}

// A stored hash this build cannot read is reported as itself rather than as a
// wrong password, so that it shows up in the log as the operational problem it
// is.
func TestAuthenticateReportsAnUnreadableHash(t *testing.T) {
	service, store := testService(t)

	if _, err := store.CreateUser(t.Context(), Record{
		User:         User{Email: "player@example.com"},
		PasswordHash: "not a hash at all",
	}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	_, err := service.Authenticate(t.Context(), "player@example.com", "a good long password")
	if err == nil {
		t.Fatal("Authenticate() = nil error, want failure")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Authenticate() error = %v, want a malformed-hash error", err)
	}
}
