-- Games in progress.
--
-- state is Result.State exactly as the engine returned it: a complete,
-- self-contained snapshot, stored whole and never parsed, compressed or
-- edited. It is NULL only once the story has halted, when there is genuinely
-- nothing left to resume.
--
-- story_key is the SHA-256 of the story image the state was written from. A
-- state only restores into a machine built from that exact file, so the two
-- belong in the same row.
--
-- version is bumped by every successful turn and is what makes the write
-- conditional: an UPDATE that matches on the version it read cannot overwrite
-- a turn that got there first.
CREATE TABLE games (
    id         INTEGER PRIMARY KEY,
    story_key  BLOB    NOT NULL,
    state      BLOB,
    turn       INTEGER NOT NULL DEFAULT 0,
    version    INTEGER NOT NULL DEFAULT 0,
    halted     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,

    CHECK (length(story_key) = 32),
    CHECK (halted IN (0, 1)),
    CHECK (halted = 1 OR (state IS NOT NULL AND length(state) > 0))
);
