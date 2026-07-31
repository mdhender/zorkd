package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"

	"github.com/mdhender/zorkd/internal/auth"
	"github.com/mdhender/zorkd/internal/invite"
)

// Invitations stores invitations to register. It implements [invite.Store].
//
// Rows are keyed by the SHA-256 of the invitation token. The token itself is
// never written here, so a copy of this database yields nothing that can be
// redeemed.
type Invitations struct {
	db *DB
}

var _ invite.Store = (*Invitations)(nil)

// CreateInvitation stores an invitation under the token's hash.
func (i *Invitations) CreateInvitation(ctx context.Context, tokenHash []byte, invitation invite.Invitation) error {
	conn, release, err := i.db.conn(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = sqlitex.Execute(conn,
		`INSERT INTO invitations (token_hash, email, created_at, expires_at) VALUES (?, ?, ?, ?);`,
		&sqlitex.ExecOptions{Args: []any{tokenHash, invitation.Email, now(), stamp(invitation.ExpiresAt)}})
	if err != nil {
		return fmt.Errorf("database: create invitation: %w", err)
	}

	return nil
}

// InvitationByToken returns the invitation stored under the token's hash.
//
// The lookup is an ordinary indexed match rather than a constant-time compare.
// The token is 256 bits of randomness rather than a secret somebody chose, so
// this is a lookup and not the comparison of one secret against another.
func (i *Invitations) InvitationByToken(ctx context.Context, tokenHash []byte) (invite.Invitation, error) {
	conn, release, err := i.db.conn(ctx)
	if err != nil {
		return invite.Invitation{}, err
	}
	defer release()

	var (
		stored invite.Invitation
		found  bool
		bad    error
	)

	err = sqlitex.Execute(conn,
		`SELECT email, expires_at, redeemed_at, user_id FROM invitations WHERE token_hash = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{tokenHash},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				stored, bad = readInvitation(stmt)
				return nil
			},
		})
	if err != nil {
		return invite.Invitation{}, fmt.Errorf("database: invitation lookup: %w", err)
	}
	if !found {
		return invite.Invitation{}, invite.ErrNotInvited
	}
	if bad != nil {
		return invite.Invitation{}, fmt.Errorf("database: invitation lookup: %w", bad)
	}

	return stored, nil
}

// Redeem marks the invitation spent and creates the account it was issued for.
//
// The read, the account and the write are one transaction, and the invitation
// is read again inside it rather than taken on trust from the caller's lookup:
// without that, two registrations racing on one token would each find it
// unspent and both create an account. It is the same reasoning as the
// count-and-write in [Sessions.CreateSave], and the write is conditional on the
// invitation still being unredeemed for the same reason a turn's write is
// conditional on the version it read.
func (i *Invitations) Redeem(ctx context.Context, tokenHash []byte, at time.Time, record auth.Record) (user auth.User, err error) {
	conn, release, err := i.db.conn(ctx)
	if err != nil {
		return auth.User{}, err
	}
	defer release()

	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return auth.User{}, fmt.Errorf("database: redeem invitation: %w", err)
	}
	defer end(&err)

	var (
		id       int64
		stored   invite.Invitation
		found    bool
		unusable error
	)

	err = sqlitex.Execute(conn,
		`SELECT id, email, expires_at, redeemed_at, user_id FROM invitations WHERE token_hash = ?;`,
		&sqlitex.ExecOptions{
			Args: []any{tokenHash},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				id = stmt.GetInt64("id")
				stored, unusable = readInvitation(stmt)
				return nil
			},
		})
	if err != nil {
		err = fmt.Errorf("database: redeem invitation: %w", err)
		return auth.User{}, err
	}
	if unusable != nil {
		err = fmt.Errorf("database: redeem invitation: %w", unusable)
		return auth.User{}, err
	}

	// Unknown, expired, already redeemed, or issued for another address: one
	// answer, decided here as well as by the caller, because this is the check
	// that runs while the write lock is held.
	if !found || !stored.RedeemedAt.IsZero() || !at.Before(stored.ExpiresAt) || stored.Email != record.Email {
		err = invite.ErrNotInvited
		return auth.User{}, err
	}

	created := now()
	err = sqlitex.Execute(conn,
		`INSERT INTO users (email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?);`,
		&sqlitex.ExecOptions{Args: []any{record.Email, record.PasswordHash, created, created}})
	if err != nil {
		if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
			err = fmt.Errorf("database: redeem invitation: %w", auth.ErrEmailTaken)
			return auth.User{}, err
		}
		err = fmt.Errorf("database: redeem invitation: %w", err)
		return auth.User{}, err
	}
	userID := conn.LastInsertRowID()

	err = sqlitex.Execute(conn,
		`UPDATE invitations SET redeemed_at = ?, user_id = ? WHERE id = ? AND redeemed_at IS NULL;`,
		&sqlitex.ExecOptions{Args: []any{stamp(at), userID, id}})
	if err != nil {
		err = fmt.Errorf("database: redeem invitation: %w", err)
		return auth.User{}, err
	}
	if conn.Changes() != 1 {
		err = fmt.Errorf("database: redeem invitation %d: %w", id, invite.ErrNotInvited)
		return auth.User{}, err
	}

	return auth.User{ID: strconv.FormatInt(userID, 10), Email: record.Email}, nil
}

// DeleteSpentInvitations removes invitations nobody can use any more.
//
// Redeemed rows go by when they were redeemed rather than by when they would
// have expired, so the grace period the caller asked for is the grace period
// they get.
func (i *Invitations) DeleteSpentInvitations(ctx context.Context, expiredBefore, redeemedBefore time.Time) (int, error) {
	conn, release, err := i.db.conn(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	err = sqlitex.Execute(conn,
		`DELETE FROM invitations
		  WHERE (redeemed_at IS NULL AND expires_at < ?)
		     OR (redeemed_at IS NOT NULL AND redeemed_at < ?);`,
		&sqlitex.ExecOptions{Args: []any{stamp(expiredBefore), stamp(redeemedBefore)}})
	if err != nil {
		return 0, fmt.Errorf("database: delete spent invitations: %w", err)
	}

	return conn.Changes(), nil
}

// readInvitation reads the columns an invitation is made of.
//
// redeemed_at and user_id are NULL while the invitation is unspent, which read
// back as the empty string and zero and stay the zero time and the empty
// identifier.
func readInvitation(stmt *sqlite.Stmt) (invite.Invitation, error) {
	invitation := invite.Invitation{Email: stmt.GetText("email")}

	expires := stmt.GetText("expires_at")
	parsed, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return invite.Invitation{}, fmt.Errorf("unreadable expiry %q", expires)
	}
	invitation.ExpiresAt = parsed

	if redeemed := stmt.GetText("redeemed_at"); redeemed != "" {
		parsed, err := time.Parse(time.RFC3339, redeemed)
		if err != nil {
			return invite.Invitation{}, fmt.Errorf("unreadable redemption %q", redeemed)
		}
		invitation.RedeemedAt = parsed
	}

	if id := stmt.GetInt64("user_id"); id != 0 {
		invitation.UserID = strconv.FormatInt(id, 10)
	}

	return invitation, nil
}
