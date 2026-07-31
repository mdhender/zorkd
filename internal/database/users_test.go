package database

import (
	"context"
	"errors"
	"testing"

	"github.com/mdhender/zorkd/internal/auth"
)

// testUser registers an account and returns its identifier. Games have an owner
// now, and the foreign key means the owner has to be real.
func testUser(t *testing.T, db *DB, email string) string {
	t.Helper()

	service, err := auth.NewService(db.Users())
	if err != nil {
		t.Fatalf("auth.NewService() error = %v", err)
	}

	user, err := service.Register(context.Background(), email, "a good long password")
	if err != nil {
		t.Fatalf("Register(%q) error = %v", email, err)
	}

	return user.ID
}

func TestUsersRoundTrip(t *testing.T) {
	ctx := context.Background()
	users := testDB(t).Users()

	created, err := users.CreateUser(ctx, auth.Record{
		User:         auth.User{Email: "player@example.com"},
		PasswordHash: "$argon2id$not-really-a-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateUser() assigned no identifier")
	}

	byEmail, err := users.UserByEmail(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("UserByEmail() error = %v", err)
	}
	if byEmail.ID != created.ID || byEmail.PasswordHash != "$argon2id$not-really-a-hash" {
		t.Errorf("UserByEmail() = %+v, want the account that was created", byEmail)
	}

	byID, err := users.UserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("UserByID() error = %v", err)
	}
	if byID.Email != "player@example.com" {
		t.Errorf("UserByID() email = %q, want %q", byID.Email, "player@example.com")
	}
}

// The UNIQUE index is what refuses a second account, rather than a check made
// first: two registrations racing for the same address must not both win.
func TestUsersRefuseADuplicateEmail(t *testing.T) {
	ctx := context.Background()
	users := testDB(t).Users()

	record := auth.Record{User: auth.User{Email: "player@example.com"}, PasswordHash: "hash"}

	if _, err := users.CreateUser(ctx, record); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if _, err := users.CreateUser(ctx, record); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("CreateUser() error = %v, want %v", err, auth.ErrEmailTaken)
	}
}

func TestUsersMissing(t *testing.T) {
	ctx := context.Background()
	users := testDB(t).Users()

	if _, err := users.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("UserByEmail() error = %v, want %v", err, auth.ErrUserNotFound)
	}

	for _, id := range []string{"1", "0", "-3", "", "abc", "1; DROP TABLE users"} {
		if _, err := users.UserByID(ctx, id); !errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("UserByID(%q) error = %v, want %v", id, err, auth.ErrUserNotFound)
		}
	}
}

// A password that was registered against the database verifies when it is read
// back out. This is the whole of what an account is for.
func TestRegisteredPasswordVerifies(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	service, err := auth.NewService(db.Users())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, err := service.Register(ctx, "Player@Example.COM ", "a good long password"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Registered with mixed case and a trailing space; found by the normalized
	// form, whatever the player types next time.
	user, err := service.Authenticate(ctx, "player@example.com", "a good long password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if user.Email != "player@example.com" {
		t.Errorf("email = %q, want the normalized address", user.Email)
	}

	if _, err := service.Authenticate(ctx, "player@example.com", "the wrong password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("Authenticate() with a wrong password error = %v, want %v", err, auth.ErrInvalidCredentials)
	}

	// The stored column is a hash, not the password.
	if got := pragma(t, db, "SELECT password_hash FROM users;"); got == "a good long password" {
		t.Fatal("the password was stored in plaintext")
	} else if len(got) < 32 {
		t.Errorf("password_hash = %q, which is not an argon2 hash", got)
	}
}
