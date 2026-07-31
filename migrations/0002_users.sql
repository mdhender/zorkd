-- Accounts.
--
-- email holds the normalized form used for lookup, so the UNIQUE index is what
-- actually prevents two accounts for the same address rather than a check the
-- application has to remember to make.
--
-- password_hash is a PHC-encoded Argon2id string: the algorithm, its cost
-- parameters and the salt travel inside it, so a hash written by an older
-- deployment still verifies after the parameters are raised.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,

    CHECK (length(email) > 0),
    CHECK (length(password_hash) > 0)
);

-- Browser sessions.
--
-- token_hash is the SHA-256 of the cookie value and never the value itself.
-- Somebody who reads this table cannot log in as anyone: the token exists only
-- in the browser that holds it, and a stolen backup yields nothing to replay.
--
-- expires_at is enforced by the application rather than by the schema, because
-- the clock is not SQLite's to keep.
CREATE TABLE auth_sessions (
    id         INTEGER PRIMARY KEY,
    token_hash BLOB    NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL,
    created_at TEXT    NOT NULL,
    expires_at TEXT    NOT NULL,

    CHECK (length(token_hash) = 32),

    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX auth_sessions_user ON auth_sessions (user_id);

-- Games belong to somebody.
--
-- Migration 0001 shipped before there were accounts and before there was a
-- server that could start a game, so no row here has an owner and none can be
-- given one. The table is rebuilt rather than backfilled.
DROP TABLE games;

CREATE TABLE games (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL,
    story_key  BLOB    NOT NULL,
    state      BLOB,
    turn       INTEGER NOT NULL DEFAULT 0,
    version    INTEGER NOT NULL DEFAULT 0,
    halted     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,

    CHECK (length(story_key) = 32),
    CHECK (halted IN (0, 1)),
    CHECK (halted = 1 OR (state IS NOT NULL AND length(state) > 0)),

    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Every game lookup is scoped to its owner, so that is the index.
CREATE INDEX games_user ON games (user_id);
