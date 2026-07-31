-- Named saves.
--
-- A save is a whole screen and not only a state. Restoring promotes these bytes
-- to the game's active state, and a transcript left over from after the save
-- point would then be describing a game that is no longer there — so the
-- transcript, the status line and the move count travel with the state and go
-- back with it.
--
-- state is written whole and read back whole. It is not compressed here:
-- dynamic memory is already stored as a run-length-compressed difference
-- against the story file, so a second pass buys almost nothing. It is not
-- parsed here either — nothing in it may be, and the columns beside it are
-- reported by the engine rather than recovered from these bytes.
--
-- story_key is the SHA-256 of the story image the state was written from. A
-- state only restores into a machine built from that exact file, so the
-- identity is stored beside the bytes and checked before a restore rather than
-- assumed from the game the row hangs off.
--
-- Every save belongs to one game, and a game belongs to one user, so ownership
-- is a join rather than a column that could disagree with one. Deleting a game
-- takes its saves with it: each state stands alone, but none of them means
-- anything without the story and the game they came from.
CREATE TABLE saves (
    id               INTEGER PRIMARY KEY,
    game_id          INTEGER NOT NULL,
    name             TEXT    NOT NULL,
    story_key        BLOB    NOT NULL,
    state            BLOB    NOT NULL,
    transcript       TEXT    NOT NULL DEFAULT '',
    turn             INTEGER NOT NULL DEFAULT 0,
    status_available INTEGER NOT NULL DEFAULT 0,
    status_name      TEXT    NOT NULL DEFAULT '',
    status_time_game INTEGER NOT NULL DEFAULT 0,
    status_score     INTEGER NOT NULL DEFAULT 0,
    status_moves     INTEGER NOT NULL DEFAULT 0,
    status_hours     INTEGER NOT NULL DEFAULT 0,
    status_minutes   INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL,

    CHECK (length(name) > 0),
    CHECK (length(story_key) = 32),
    CHECK (length(state) > 0),

    FOREIGN KEY (game_id) REFERENCES games(id) ON DELETE CASCADE
);

-- Names are unique within a game and are matched without regard to case, so a
-- player who saved "Troll" and then saves "troll" replaces it rather than
-- ending up with two saves that read the same in a list. NOCASE folds ASCII
-- only, which is the same fold the application applies.
CREATE UNIQUE INDEX saves_game_name ON saves (game_id, name COLLATE NOCASE);

-- Saves are listed for one game, newest first.
CREATE INDEX saves_game ON saves (game_id, created_at DESC);
