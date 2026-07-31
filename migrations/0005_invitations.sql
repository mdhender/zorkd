-- Invitations to register.
--
-- token_hash is the SHA-256 of the token and never the token itself. The token
-- exists in the link handed to a person and nowhere else: it is printed once
-- when the invitation is created, and a lost one is reissued rather than looked
-- up. An unsalted, fast hash is right here and would be wrong for a password —
-- the token is 256 bits of randomness rather than something somebody chose, so
-- there is nothing to guess and nothing to build a table against, which is also
-- why the lookup is an ordinary indexed match rather than a constant-time
-- compare.
--
-- email is stored in plaintext, in the same normalized form users.email holds.
-- The reasoning that makes hashing right for the token is what makes it wrong
-- here: an address is low-entropy and enumerable, so its SHA-256 is reversible
-- with a word list and would look like protection while providing almost none.
-- users.email is already plaintext beside it, and the address has to be
-- readable — the registration form says which address the invitation is for,
-- and whoever issues invitations has to be able to see who has one outstanding.
-- What hashing would buy is answered better by the TTL and the reaper: those
-- rows are deleted rather than obscured.
--
-- Single use, and expiring. Both are the conservative default and neither is
-- hard to relax later. There is deliberately no unique index on email: the same
-- address may legitimately be invited twice, after one expired unused or
-- because the link was lost. Uniqueness of accounts is users.email's job and
-- stays there.
--
-- A redeemed invitation names the account it made, and goes when that account
-- does: a user's games already cascade, and a redeemed invitation belonging to
-- a deleted account is not a record worth keeping.
CREATE TABLE invitations (
    id          INTEGER PRIMARY KEY,
    token_hash  BLOB NOT NULL UNIQUE,   -- SHA-256 of the token, never the token
    email       TEXT NOT NULL,          -- normalized, as users.email is
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    redeemed_at TEXT,
    user_id     INTEGER,                -- the account it created, once redeemed

    CHECK (length(token_hash) = 32),
    CHECK (length(email) > 0),

    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX invitations_expires ON invitations (expires_at);
