-- What the player is looking at.
--
-- The saved state carries no transcript and no status line, and nothing in it
-- may be parsed to recover either, so a browser that refreshes has nothing to
-- redraw unless the screen is kept beside the state. Both are written in the
-- same turn as the state they belong to, so what is drawn cannot disagree with
-- what will be played.
--
-- transcript is stored as the story wrote it, with the player's own lines
-- interleaved where the terminal showed them; wrapping and escaping are the
-- presentation's work. It is bounded — the application trims the oldest lines,
-- so a long game does not become a large row.
--
-- The status columns are the engine's reported status line, which is data
-- rather than something it printed. They are flat columns and not a blob,
-- because the alternative is a format this application would have to parse.
-- status_score and status_moves apply to a score game; status_hours and
-- status_minutes to a time game. Zork is a score game.
ALTER TABLE games ADD COLUMN transcript       TEXT    NOT NULL DEFAULT '';
ALTER TABLE games ADD COLUMN status_available INTEGER NOT NULL DEFAULT 0;
ALTER TABLE games ADD COLUMN status_name      TEXT    NOT NULL DEFAULT '';
ALTER TABLE games ADD COLUMN status_time_game INTEGER NOT NULL DEFAULT 0;
ALTER TABLE games ADD COLUMN status_score     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE games ADD COLUMN status_moves     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE games ADD COLUMN status_hours     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE games ADD COLUMN status_minutes   INTEGER NOT NULL DEFAULT 0;
