# Zork Web Server — Project Specification

**Status:** Implementation specification  
**Working name:** `zorkd`  
**Primary language:** Go 1.26 or later  
**Execution engine:** `github.com/maloquacious/zmachine` v0.2.0 (pinned)  
**Database:** SQLite via ZombieZen (`zombiezen.com/go/sqlite`)  
**Frontend:** Server-rendered HTML + HTMX + Alpine.js  
**Game target:** Zork I, Zork II and Zork III, executed from their original `.z3` story files

---

## 1. Purpose

Build a self-contained Go web application that runs the original Infocom **Zork** trilogy on the server and presents it to authenticated users through a browser interface modeled after an early-1980s computer terminal.

The application owns:

- user authentication;
- per-user game state;
- the turn cycle around the Z-machine engine;
- automatic persistence after player input;
- explicit named saves and restores;
- terminal rendering, including word wrapping and the status line;
- static web assets.

The application does **not** own the Z-machine. Version 3 execution comes from `github.com/maloquacious/zmachine`, an external, separately tested package. This project is its host.

The browser does **not** run the Z-machine either. It acts as a thin terminal client.

This is explicitly **not** a general-purpose interactive-fiction platform. The engine's compatibility is validated on Zork I, II and III; the wider Version 3 catalog is unproven and out of scope until that testing is budgeted.

---

## 2. Design Principles

### 2.1 Zork first

Running Zork I, II and III reliably is the primary requirement. General interactive-fiction compatibility is secondary and, for now, out of scope.

Do not build for other story files, other Z-machine versions, or a story catalog merely for completeness.

When a choice exists between:

1. a small, well-tested implementation sufficient for the Zork trilogy; and
2. a generalized abstraction anticipating future games or engines;

prefer the first unless the generalized design is equally simple.

### 2.1.1 Host, not interpreter

Z-machine behavior is the engine's responsibility. This application supplies input, renders output, and stores state between turns.

An apparent interpreter bug is investigated and then reported upstream at `maloquacious/zmachine` with the story file, the exact input sequence, and the previous turn's saved state. It is not worked around locally.

Do not describe the engine as conforming to Z-machine Standard 1.1 in product copy. It implements Version 3, is written against Standard 1.1, and deliberately leaves the header's standard revision number unset.

### 2.2 Server-authoritative state

All authoritative game state resides on the server.

A user may close the browser, switch devices, restart the browser, or reconnect later and continue the same game.

The browser must not be required to preserve authoritative state in `localStorage`, IndexedDB, cookies, or JavaScript memory.

### 2.3 Persist at input boundaries

The natural transaction boundary is a Z-machine input request.

Conceptually:

```text
authenticated request
        |
        v
load persisted machine state
        |
        v
supply player command
        |
        v
run Z-machine until next external boundary
        |
        v
persist resulting machine state
        |
        v
render response
```

The server should not require a permanently running goroutine, process, or machine for each player. Rebuilding the machine every turn is observably identical to keeping one open; the engine's own integration tests assert it.

### 2.4 Thin browser

Use server-rendered HTML wherever practical.

Use **HTMX** for request/response interaction and fragment replacement.

Use **Alpine.js** only for small amounts of browser-local behavior that would otherwise be awkward, such as:

- command history navigation;
- input focus;
- terminal preferences;
- optional CRT effects;
- small UI state transitions.

Do not introduce React, Vue, Svelte, Ember, or another SPA framework.

### 2.5 Boring infrastructure

The target deployment should require approximately:

```text
zorkd
zork.db
```

plus configuration/secrets as appropriate.

The application should use Go's standard `net/http` server unless a concrete requirement justifies another HTTP framework.

SQLite is the system of record.

---

## 3. High-Level Architecture

```text
Browser
+------------------------------------------------+
| Server-rendered terminal                       |
|                                                |
| HTMX: commands, login, save/restore fragments  |
| Alpine: keyboard/history/local presentation    |
+----------------------+-------------------------+
                       |
                     HTTPS
                       |
                       v
Go Server
+------------------------------------------------+
| net/http                                       |
|                                                |
| authentication / session management            |
|              |                                 |
|              v                                 |
| game application service                       |
|   command interception, turn cycle, rendering  |
|              |                                 |
|       +------+-------+                         |
|       |              |                         |
|       v              v                         |
| zmachine engine   persistence                  |
| (external pkg)    ZombieZen SQLite             |
|       |              |                         |
|       v              v                         |
| games/*.z3        zork.db                      |
+------------------------------------------------+
```

The HTTP layer must not import the engine or contain Z-machine details.

The engine knows nothing about HTTP, authentication, users, HTMX, HTML, or SQLite, and it is not this project's job to teach it.

Persistence stores the engine's `Result.State` as an opaque blob. Nothing in this application parses it.

---

## 4. Technology Choices

### 4.1 Go

Use Go 1.26 or later. The engine requires it.

Prefer the standard library where it is sufficient, particularly:

- `net/http`;
- `html/template`;
- `embed`;
- `context`;
- `log/slog`;
- `crypto/*`.

Third-party packages should have a clear purpose and should not replace simple standard-library functionality without benefit.

### 4.2 Z-machine engine

Use:

```text
github.com/maloquacious/zmachine v0.2.0
```

Pin the tag. The engine is `v0.x` and its exported API has already moved once — `ExecutionError.Op` changed type in 0.2.0 — so a minor bump may break the build before `v1.0.0`. Read the engine's `CHANGELOG.md` on every version bump.

The engine imports nothing outside the standard library and `github.com/maloquacious/quetzal`.

Saved state is a stronger promise than the API: a `Result.State` stays restorable for as long as the story file is unchanged, whatever engine version wrote it. Stored state never needs migrating and the producing build never needs recording.

The engine's own documentation is authoritative for its behavior:

- `docs/tutorial.md` — three turns of Zork I, rebuilding the machine between each;
- `docs/how-to/` — persisting state, cancelled requests, concurrent players;
- `docs/reference.md` — lifecycle, options, `Result`, errors, concurrency, limits;
- `pkg.go.dev` — per-symbol signatures.

### 4.3 SQLite

Use:

```text
zombiezen.com/go/sqlite
```

Do **not** use `modernc.org/sqlite`.

Use SQLite in WAL mode.

Database access should be explicit and small. A large ORM is not desired.

Schema changes must be represented by versioned migrations.

### 4.4 HTMX

HTMX is the primary browser/server interaction mechanism.

Typical command submission:

```html
<form
  hx-post="/game/input"
  hx-target="#terminal"
  hx-swap="beforeend">
```

The exact markup may evolve, but the interaction model should remain HTML-over-the-wire rather than JSON API + client-side application rendering.

### 4.5 Alpine.js

Alpine.js supplements HTMX.

Appropriate uses include:

- Up/Down command history;
- restoring focus to the command line;
- toggling amber/green display;
- toggling scanlines or glow;
- small modal/menu state.

Alpine must not become a second application architecture.

### 4.6 Embedded assets

Where licensing permits, use `//go:embed` for templates and static application assets so deployment remains simple.

**The Zork story images are embedded.** They ship inside the executable, so deployment is the binary and its database, and a server can never come up missing the game it was meant to serve. The three files are ~267 KB together, which is not a reason to make deployment harder.

The `games` package owns the embedding and exposes the images as data. Nothing else names a story file, so a story loaded from elsewhere later is a change to one package.

Do not add a configuration path that reads story files from disk unless something concrete requires it. Licensing already permits shipping these three; a story that cannot be shipped is a different problem, and `games/local/` is where a maintainer keeps one.

---

## 5. Game Runtime Model

### 5.1 Story images

The application loads each supported `.z3` story image once, at startup:

```go
story, err := zmachine.LoadStory(data)
```

`LoadStory` validates the entire image, checks every header address and table extent, and rejects any version other than 3. It copies the bytes it is given, so the embedded slice is never modified by execution.

The resulting `*zmachine.Story` is immutable, safe for concurrent use, and kept for the life of the process. Loading is the expensive step; everything after it is per request.

Key each loaded story by a **SHA-256 over the story image**, taken before `LoadStory` is called. Do not key by release and serial: those identify an edition rather than a file, and `Story.Checksum()` is 0 for early Version 3 stories that carry none.

### 5.2 Machine creation

A `*zmachine.Machine` is created from a `*Story` per request:

```go
machine, err := zmachine.New(story,
    zmachine.WithLogger(logger),
    zmachine.WithInstructionLimit(5_000_000),
)
```

`New` is cheap: it copies only dynamic memory — 11,282 bytes of Zork I's 86,838 — and shares the rest of the image with the `Story`.

A `Machine` owns all mutable execution state and is **not** safe for concurrent use. One machine belongs to one goroutine for the duration of one call. Do not cache or pool machines between turns.

### 5.3 Execution

Two calls advance a story, and each runs until the next input boundary or termination:

```go
func (m *Machine) Start(ctx context.Context) (Result, error)
func (m *Machine) Run(ctx context.Context, input string) (Result, error)
```

`Start` begins a new game and supplies no input. `Run` supplies one line. The valid sequences are:

```text
new game:      New → Start                  → Result
resumed turn:  New → Restore → Run(command) → Result
```

`Result` carries what the host needs:

| Field | Meaning |
| --- | --- |
| `Output` | Story text, whitespace preserved exactly. Never the status line. |
| `UpperWindow` | Text printed to the upper window, with no cursor positions attached. |
| `StatusLine` | Room, score and turns as of the moment execution stopped. |
| `State` | Resumable state. Non-nil when waiting for input; nil when halted. |
| `Status` | `WaitingForInput` or `Halted`. |

There is no event stream and no callback into host code. The boundary is the return of the call.

### 5.4 Input lifecycle

For a normal command:

1. Authenticate the request.
2. Locate the user's active game and take the per-session lock.
3. Intercept `SAVE`, `RESTORE` and any other host-owned command (section 13). These never reach the engine.
4. Load the persisted state and the story it belongs to.
5. `zmachine.New(story)` with a request-scoped context deadline and an instruction limit.
6. `machine.Restore(saved)`.
7. `machine.Run(ctx, command)`.
8. On success, atomically persist `result.State` and render `result.Output`, `result.UpperWindow` and `result.StatusLine`.
9. Discard the machine.

A failed execution must not overwrite the last known-good persisted state. On any error the `Result` is unusable — there is no partial turn to salvage — and the previously stored state is still exactly right, because the machine that failed was a copy and nothing outside it changed.

Write nothing to storage on the failure path.

### 5.4.1 Failure classification

Test for context cancellation first: it returns the context's own error, unwrapped, and wraps no engine sentinel. A `default` branch that assumes an engine error will report a disconnected client as an interpreter bug.

| Condition | Response |
| --- | --- |
| `context.Canceled` | The client is gone. Log it and return; there is nobody to answer. |
| `context.DeadlineExceeded` | Tell the player the turn did not happen. Safe to retry with a fresh context, the same stored state and the same command. |
| `ErrExecutionLimit` | Not transient — a retry stops in the same place. Report it; do not replay automatically. |
| `ErrInvalidState` | The bytes are damaged or belong to a different story. Check the stored story key before assuming corruption. |
| `ErrExecutionFault`, `ErrInvalidOpcode` | Log with the program counter from `*ExecutionError` and report upstream. |

A cancelled or timed-out turn is not a game over. The session is intact.

### 5.4.2 Fault logging

Log `ErrExecutionFault` and `ErrInvalidOpcode` with the program counter from the first deployment.

The engine's deepest test route is roughly 46 turns plus a longer multi-seed route. Opening and mid-game are well covered; no test finishes a game, so late-game code paths have never executed. Silence is not evidence of correctness in territory nothing has walked yet.

### 5.5 Automatic continuation

A player does not need to type `SAVE` merely to leave the site.

Every completed input cycle updates the active game state.

Therefore:

```text
> open mailbox
```

followed by closing the browser must be sufficient for the player to resume after `open mailbox` on the next login.

---

## 6. Engine Scope and Host Responsibilities

### 6.1 What the engine provides

Version 3 execution, complete: the full opcode set, memory, objects and properties, dictionary and tokenization, Z-string decoding, the evaluation stack and call frames, random numbers, input boundaries, and resumable state. `LoadStory` rejects every version other than 3.

Story files and saved states are treated as hostile binary input. Every address, length and count is checked before it is used to index, allocate or slice, and malformed input is an error rather than a panic.

### 6.2 What the host owns

These are host responsibilities by design, not engine gaps to wait on.

**Rendering.** No word wrapping, no screen width, no cursor model. `Result.Output` preserves the story's whitespace exactly, and the roughly 80-column terminal presentation is this application's work. Do not insert newlines into story text in a way that corrupts its own whitespace.

**The upper window.** `Result.UpperWindow` is a separate string with no cursor positions attached. Presenting it is this application's work.

**The status line.** `Result.StatusLine` is reported, never printed. Check `Available` before using any other field. Zork is a score game, so `Score` and `Turns` are the meaningful fields; `Hours` and `Minutes` belong to time games.

**Saving.** In-story `SAVE` and `RESTORE` report failure without branching. See section 13.

**Transport, users, persistence, transactions, retries, idempotency, concurrency control.** The engine has no filesystem, no network, no environment, and never touches process state.

### 6.3 Compatibility strategy

Compatibility is validated upstream on Zork I, II and III, and differentially against dfrotz on Zork I: transcripts, status lines and game state all match, and dfrotz successfully resumes a state this engine wrote.

This application's job is to notice and report divergence, not to correct it. A suspected interpreter bug becomes a regression test here and an upstream issue there.

### 6.4 Determinism

Determinism under test comes from `zmachine.WithRandomSeed(seed)`. Two machines given the same story and the same seed produce the same sequence.

Do not invent a local randomness abstraction. Without the option the generator is seeded unpredictably, which is what a real game wants, and the generator's state travels inside `Result.State`, so a restored session continues its sequence rather than starting a fresh one.

`WithFrotzRandomSeed` exists for differential comparison against Frotz and is not something this application needs.

---

## 7. Persistence

### 7.1 Database

SQLite is authoritative for:

- users;
- authenticated sessions if server-side sessions are used;
- active games;
- named saves;
- schema version.

Enable WAL mode during database initialization.

Use transactions where game-state updates require atomicity.

### 7.2 Suggested schema

The final schema may differ, but begin with approximately:

```sql
CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    email       TEXT NOT NULL UNIQUE,       -- normalized: trimmed and lowercased
    password_hash TEXT NOT NULL,            -- PHC-encoded Argon2id
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE auth_sessions (
    id          INTEGER PRIMARY KEY,
    token_hash  BLOB NOT NULL UNIQUE,       -- SHA-256 of the cookie value
    user_id     INTEGER NOT NULL,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,

    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE games (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    story_key   BLOB NOT NULL,              -- SHA-256 of the story file
    state       BLOB,                       -- Result.State; NULL once halted
    turn        INTEGER NOT NULL DEFAULT 0,
    version     INTEGER NOT NULL DEFAULT 0, -- bumped every turn; see section 9
    halted      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,

    CHECK (halted = 1 OR (state IS NOT NULL AND length(state) > 0)),

    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE saves (
    id          INTEGER PRIMARY KEY,
    game_id     INTEGER NOT NULL,
    name        TEXT NOT NULL,
    state       BLOB NOT NULL,
    created_at  TEXT NOT NULL,

    FOREIGN KEY (game_id) REFERENCES games(id),
    UNIQUE (game_id, name)
);
```

Add indexes based on actual query patterns.

Foreign-key enforcement must be enabled.

### 7.3 Saved state

The engine owns state serialization. This application stores bytes.

`Result.State` is a complete, self-contained snapshot — not a delta, not a link in a chain — so the most recent one is the only one a session needs. Nothing has to be replayed, and states may be deleted in any order.

Rules:

- write the state whole, replacing whatever was stored last turn;
- use a `BLOB` column, not a fixed-width one. A Zork I state is 356 bytes at the opening prompt and around 500 a few turns in; it grows as dynamic memory diverges from the story file, not with the number of turns;
- do not compress it. Dynamic memory is already a run-length-compressed difference against the story file, so a second pass buys almost nothing;
- do not parse, edit or inspect it. Do not recover the score or room name from it — `Result.StatusLine` reports those. Any field recovered by hand ties this application to a format that is free to change;
- store the story key (section 5.1) beside it. A state only restores into a machine built from the story it was saved from;
- do not store which engine version wrote it. A state restores for as long as the story file is byte-identical, so stored state needs no migration on an engine upgrade;
- `Result.State` is nil when `Result.Status` is `Halted`. Do not overwrite a good state with nil unless the session is deliberately being closed — storing nil turns the next restore into a failure that reads as corruption.

For undo or multiple save slots, keep more than one row. Each state stands alone, so any of them restores by itself. Do not build anything on top of the format.

### 7.4 Quetzal

The saved state happens to be in the Quetzal format, and `github.com/maloquacious/quetzal` is an indirect dependency through the engine. Neither fact is this application's to use.

Do not import `quetzal` directly, and do not treat the state as anything but opaque bytes.

Interoperability with other interpreters is a possible future feature, not a current requirement. The engine already accepts foreign saves, moving their program counter to its own input boundary; exposing that as a product feature is a separate decision.

---

## 8. Authentication and Authorization

### 8.1 User identity

Users authenticate with an email address and password.

Normalize email addresses consistently for lookup.

Passwords must be stored using an appropriate password hashing algorithm. Never store plaintext passwords.

**The decision.** Argon2id, from `golang.org/x/crypto/argon2`, at OWASP's current parameters: 19 MiB of memory, two passes, one lane, a 16-byte salt and a 32-byte key. Hashes are stored in the PHC string form, so the algorithm, its parameters and the salt travel inside the hash and raising the cost later does not invalidate what is already stored.

Normalization is a trim and a lowercase, applied by `auth.NormalizeEmail` on both registration and login. Only a bare address is accepted; a display form such as `Player <player@example.com>` is refused rather than silently reduced.

An unknown address and a wrong password are the same answer, `ErrInvalidCredentials`, and both cost a password verification. An early return would make a failed login measurably faster for addresses with no account, which is a way of asking the server who its users are.

### 8.2 Sessions

Use secure HTTP-only cookies for browser authentication.

Requirements:

- `HttpOnly`;
- `Secure` in production;
- appropriate `SameSite` policy;
- unpredictable session identifiers;
- expiration;
- logout invalidation.

Prefer conventional server-side session semantics over JWT unless a concrete deployment requirement emerges for stateless authentication.

**The decision.** Server-side sessions, in `internal/session`. The cookie holds 256 bits from `crypto/rand`; the database holds only its SHA-256, so a copy of the database cannot be used to log in as anybody. `Secure` is the default and `WithInsecureCookies` has to be asked for by name, so a deployment fails safe. `SameSite=Lax` lets a player follow a link into the game while keeping the cookie off cross-site form posts. Logging out deletes the row as well as clearing the cookie: a copied cookie stops working.

CSRF beyond `SameSite` belongs with the forms that need it — see milestone 9. `net/http.CrossOriginProtection` covers the HTMX design without a token in every form.

### 8.3 Authorization

Every game and save operation must be scoped to the authenticated user.

Never trust a game or save identifier supplied by the browser without verifying ownership.

The application must prevent one user from reading, modifying, restoring, or deleting another user's game state.

**The decision.** The owner is a parameter of the store, not a check made afterwards: `Store.Load` takes the user, the SQL matches on `user_id`, and the conditional update matches on it too. There is no way to read a game without saying whose it is.

Another user's session is reported as `ErrSessionNotFound` rather than as a refusal. Distinguishing "not yours" from "no such thing" confirms that a game exists, which is the one fact a stranger holding an identifier is trying to learn.

---

## 9. Concurrency

Concurrency *between* sessions is free. One `*Story` backs any number of simultaneous machines, machines built from the same story share nothing but immutable memory, and the engine keeps no mutable state in a package-level variable. Two players in the same room of the same game cannot see each other.

Concurrency *within* one session is a correctness problem this application must solve, and the engine cannot solve it: two turns starting from the same stored state will both succeed, both write, and one will be overwritten. The player watches a command vanish.

A user may accidentally submit multiple commands from multiple tabs or devices.

At minimum:

- take a per-session lock for the whole read-run-write cycle. A mutex is enough for one process;
- across processes, make the write conditional on the state that was read — the `version` column bumped each turn, and an update that matches on it:

```sql
UPDATE games SET state = ?, version = version + 1, turn = ?
 WHERE id = ? AND version = ?;
```

Zero rows affected means another turn got there first. Fail that request. Do not replay it against the newer state; the player issued it against the old one.

A stale request should receive a controlled response instructing the browser to refresh the current terminal state rather than overwriting newer state.

Database transactions must preserve the last known-good state.

A `*slog.Logger` given to `WithLogger` is safe to share across machines. A `Tracer` is not — give each machine its own, or make it safe for concurrent calls.

---

## 10. Web Interface

### 10.1 Experience

The game page should evoke an early-1980s terminal rather than a modern application wearing a terminal skin.

The primary screen is text.

Example:

```text
ZORK I: The Great Underground Empire

West of House
You are standing in an open field west of a white
house, with a boarded front door.

There is a small mailbox here.

> open mailbox
Opening the small mailbox reveals a leaflet.

> _
```

### 10.2 Presentation

Desired characteristics:

- dark background;
- green phosphor default;
- optional amber phosphor;
- monospace terminal-like typeface;
- approximately 80-column presentation;
- strong keyboard focus;
- block cursor;
- scrolling transcript;
- optional restrained glow;
- optional restrained scanlines;
- responsive behavior on phones and tablets.

CRT effects must never make the text difficult to read.

The application should remain usable with decorative effects disabled.

### 10.3 Accessibility

Terminal aesthetics do not override accessibility.

Requirements include:

- adequate contrast;
- keyboard operation;
- semantic forms;
- visible focus behavior;
- reduced-motion support;
- no essential information communicated solely through visual effects.

### 10.4 JavaScript degradation

JavaScript is expected for the intended experience because HTMX and Alpine are selected technologies.

Nevertheless, the server should remain the source of truth, and browser refreshes must reconstruct the authoritative game display sufficiently to continue playing.

---

## 11. Terminal Model

The engine hands back text, not display operations, and there is no screen model to inherit. The terminal is built here.

A turn produces three things worth rendering:

| From the engine | Rendered as |
| --- | --- |
| `Result.Output` | The scrolling transcript. |
| `Result.UpperWindow` | A block of its own above the transcript, never wrapped. No cursor positions are attached. |
| `Result.StatusLine` | The status bar, drawn by this application. |

Story output is data, never trusted HTML. Escape it on the way into a template. This is the same separation the terminal-operation model was reaching for, enforced at the rendering layer instead.

### 11.1 Word wrapping

The engine performs no word wrapping and has no notion of screen width, because inserting newlines would corrupt the story's own whitespace.

Wrapping is therefore this application's work. Wrap for the roughly 80-column presentation described in section 10, and preserve the story's blank lines and leading spaces while doing it. Whitespace the story emitted deliberately — indentation, blank lines between paragraphs, the trailing `>` prompt with no newline after it — is meaningful.

Prefer wrapping in CSS where the presentation allows it, so the stored transcript keeps the story's text as the story wrote it.

**The decision:** `internal/terminal` provides both. `Wrap` folds text for a character terminal, preserving blank lines, indentation, runs of spaces and the prompt's missing trailing newline, and leaving a word longer than the width whole rather than breaking it. The HTML path inserts no newlines at all: the transcript goes into `<pre>` and is wrapped by CSS, so what is stored and what is sent stay exactly what the story wrote.

The upper window is never wrapped on either path. It overlays fixed screen positions, so folding a long line would destroy the alignment that is its only purpose; it is presented whole, in a block above the transcript, and allowed to overhang.

### 11.2 Status line

`Result.StatusLine` is reported rather than printed, and is updated in exactly two circumstances: when the story executes `show_status`, and immediately before a line-input instruction reads.

Check `Available` before using any other field; the rest are meaningless until it is true.

Zork is a score game, so render `Name`, `Score` and `Turns`. `Hours` and `Minutes` apply only when `TimeGame` is true and are not expected here.

The web UI should render the status line separately from the scrolling transcript.

### 11.3 Transcript

The server should retain enough information to reconstruct the player's useful terminal view after refresh or login.

The saved state contains no display transcript, and nothing in it may be parsed to recover one.

Possible approaches include:

- persisting terminal transcript separately;
- persisting a bounded recent transcript;
- regenerating an appropriate screen from a stored terminal model.

Choose the simplest implementation that produces reliable refresh/resume behavior.

A bounded transcript is preferred over unbounded database growth.

**The decision.** A bounded plain-text transcript and the last reported status line are stored on the game row and written in the same conditional update as the state they belong to, so what is drawn can never disagree with what will be played. The transcript holds what the story wrote with the player's own lines interleaved where the terminal showed them; wrapping and escaping are the presentation's work. It is trimmed from the oldest line once it passes `game.MaxTranscriptBytes`.

The status line is stored because a refresh plays no turn: without it the bar would be blank until the player's next command. The upper window is not stored — it is a screen overlay belonging to the turn that drew it, and Zork does not use one.

The story ends every turn with a bare `>` and no newline after it, leaving the cursor beside it. In a browser the command field *is* that cursor, so the rendering moves the prompt onto the input line rather than printing one above the other. The stored transcript keeps what the story wrote; only the rendering moves it.

---

## 12. HTMX Interaction

### 12.1 Command submission

The command line should be an ordinary HTML form enhanced with HTMX.

Conceptually:

```html
<form
    hx-post="/game/input"
    hx-target="#terminal-output"
    hx-swap="beforeend">
    <input
        name="command"
        autocomplete="off"
        autofocus>
</form>
```

The response should normally be an HTML fragment containing the echoed command and resulting output.

### 12.2 Error handling

Expected application errors should be represented cleanly in terminal UI rather than replacing the page with a generic error document.

Unexpected server errors should:

- preserve the previously stored game state;
- be logged with useful context;
- return a safe user-facing error;
- never expose stack traces or secrets.

### 12.3 Alpine responsibilities

Use Alpine for browser-local command history.

Command history need not initially be synchronized across devices.

Do not confuse command history with authoritative game state.

---

## 13. Save and Restore

There are two distinct concepts, and one product decision that shapes the UI.

### 13.1 The decision: intercept `SAVE` and `RESTORE`

In-story `SAVE` and `RESTORE` report failure without branching. That is legal Version 3 behavior and the story copes — but in Zork I the player simply sees `Failed.`

Since this application owns persistence, the game service intercepts `SAVE` and `RESTORE` **before** the input reaches `machine.Run` and wires them to session storage. The engine never sees those commands and is not involved in the save UI.

Settle the wording and flow before the first playable build; it shapes the terminal UI.

Interception must be conservative. Match the commands the player actually types for saving and restoring, pass everything else through unchanged, and never let a heuristic swallow a legitimate game command.

### 13.2 Automatic state

The active game is automatically persisted after every completed command/input cycle.

This is the normal continuation mechanism.

### 13.3 Named saves

When the player asks to save, the application presents a terminal-style UI for naming the save and stores the current state under that name.

Example:

```text
Save game as: before-troll
Game saved.
```

Named saves belong to the active game/user.

### 13.4 Restore

A restore request may present saved games in a terminal-oriented selector.

Example:

```text
Saved games:

1. before-troll
2. cellar
3. suspicious-grue

Restore which? _
```

Restoring a named save means promoting its stored bytes to the active game state. There is no engine call for it beyond the ordinary `New` + `Restore` of the next turn.

Every save row belongs to one game and one user, and every restore must verify that ownership.

### 13.5 Restart

`RESTART` is implemented by the engine: the story executes the opcode, the machine returns to its initial state, and the resulting `Result.State` is persisted like any other turn. No special host handling is required to make it work.

The web application may request confirmation before a destructive restart, but that confirmation belongs outside the engine.

---

## 14. HTTP Surface

Prefer resource-oriented, server-rendered routes.

An initial route set might be:

```text
GET   /login
POST  /login
POST  /logout

GET   /
GET   /game
POST  /game/input
POST  /game/restart

GET   /game/saves
POST  /game/saves
POST  /game/saves/{id}/restore
DELETE /game/saves/{id}
```

**What was built.** A player has more than one game — three stories, and more than one game of each — so the game routes are plural and carry an identifier:

```text
GET   /login          GET  /register
POST  /login          POST /register
POST  /logout

GET   /                       the lobby: this player's games, and the stories
POST  /games                  start a story
GET   /games/{id}             the terminal, redrawn from what is stored
POST  /games/{id}/input       one turn

GET   /games/{id}/saves                    the save prompt or the restore selector
POST  /games/{id}/saves                    write a named save
POST  /games/{id}/saves/{save}/restore     go back to one
POST  /games/{id}/saves/{save}/delete      remove one
GET   /static/...
```

`RESTART` needs no route: the engine implements the opcode, so a player types it and the resulting state is persisted like any other turn (section 13.5).

The save routes hang off the game rather than standing on their own, so ownership is one join and there is no query that could reach another player's save by being called with the wrong argument. Deletion is a `POST` because a form is all a browser without JavaScript can send.

Cross-origin protection comes from `net/http.CrossOriginProtection`, which refuses a state-changing request a browser reports as cross-site. It covers every POST without a token in every form.

These are not intended as a public JSON API.

HTMX requests should receive HTML fragments.

Normal browser navigation should receive complete HTML documents.

Avoid separate `/api/v1/...` endpoints unless an actual external API becomes a requirement.

---

## 15. Suggested Go Package Layout

A starting structure:

```text
.
├── cmd/
│   ├── zorkd/
│   │   └── main.go
│   └── zorkplay/      # terminal driver for the turn cycle; no HTTP
│       └── main.go
├── internal/
│   ├── auth/          # accounts and passwords; no HTTP
│   ├── database/
│   ├── game/          # the turn cycle; the only importer of zmachine
│   │   ├── library.go # LoadStory once per story, keyed by SHA-256
│   │   ├── turn.go    # New → Restore → Run → discard
│   │   ├── session.go # the read-run-write cycle; per-session locking
│   │   ├── memstore.go# in-memory Store, for tests and development
│   │   ├── command.go # SAVE/RESTORE interception
│   │   └── errors.go  # engine error classification
│   ├── httpserver/    # routes, handlers, views; does not import zmachine
│   ├── session/       # browser sessions; game sessions live in internal/game
│   └── terminal/      # the plain-text terminal: wrapping and the status bar
├── games/             # embedded story images + their licenses
│   ├── games.go
│   ├── zork1/
│   ├── zork2/
│   └── zork3/
├── migrations/        # embedded, versioned SQL; a package like games/
├── web/               # embedded templates and assets; a package like games/
│   ├── web.go
│   ├── static/
│   │   └── vendor/    # third-party assets, each with its license
│   └── templates/
├── testdata/
├── go.mod
├── go.sum
└── README.md
```

There is no `internal/zmachine`. Execution is an external dependency, and a local package that only renames the engine's types would be a wrapper for its own sake.

Do not treat this layout as immutable. Prefer packages that correspond to real boundaries rather than creating packages merely to organize filenames.

---

## 16. Story Licensing and Provenance

The project must document exactly where each Zork story image comes from and under what license it is distributed. Each file under `games/` carries its own `LICENSE` beside it; see `games/README.md`.

Story file names carry the release and serial number from the story's own header — `zork1-r119-880429.z3` — because a saved state only restores against the exact story it was made from. Do not rename them to bare `zorkN.z3`.

Keep separate:

- source code for this Go application;
- third-party Go/JavaScript dependencies;
- Zork source/story data;
- trademarks, logos, packaging, artwork, and other assets.

Do not assume that permission to use source code automatically grants rights to trademarks or original commercial artwork.

The build process should make it possible to omit the story image from the repository if licensing or distribution requirements make that preferable.

---

## 17. Security Requirements

At minimum:

- hash passwords using a modern password-hashing scheme;
- use cryptographically secure session identifiers;
- use secure cookies;
- enforce CSRF protection appropriate to the chosen session/HTMX design;
- escape all game output when rendering HTML;
- never treat story output as HTML;
- limit command/input size;
- limit save names and validate them as data, not filesystem paths;
- use database identifiers rather than user-controlled filenames;
- authorize every game/save operation;
- set reasonable HTTP timeouts;
- limit request body sizes;
- prevent concurrent state overwrite;
- do not expose arbitrary story-file loading to ordinary users;
- do not provide filesystem access through save/restore operations;
- bound every turn with a context deadline and an instruction limit (section 5.2), so one player cannot hold a worker.

The engine treats story files and saved states as hostile binary input: every address, length and count is checked before it is used to index, allocate or slice, and malformed input is reported as an error rather than a panic. A hostile save cannot turn a declared length into an allocation, because a restored call chain or stack can never be larger than the machine would have built itself.

That is the engine's guarantee, not a reason to skip ours. Do not assume a stored state is valid merely because this server previously produced it; persisted data can be corrupted, and `ErrInvalidState` must be handled rather than treated as impossible.

Vulnerabilities in the engine follow its `SECURITY.md` rather than its public issue tracker.

---

## 18. Logging

Use structured logging, preferably `log/slog`.

Useful fields include:

```text
request_id
user_id
game_id
turn
route
duration
error
```

Pass a `*slog.Logger` to the engine with `zmachine.WithLogger`. It receives interpreter diagnostics only; story output is never written to it, and logging never changes execution semantics. A machine created without the option discards diagnostics and never falls back to `slog.Default`.

Log `ErrExecutionFault` and `ErrInvalidOpcode` from the first deployment, with the program counter and opcode from `*zmachine.ExecutionError`, at a level that will actually be seen. Late-game code paths have never executed under test (section 5.4.2).

Do not log:

- passwords;
- session tokens;
- password hashes;
- complete database snapshots.

Player commands should not be logged by default. They may contain arbitrary user-entered text.

---

## 19. Configuration

Configuration should be small and explicit.

Possible settings:

```text
ZORK_ADDR
ZORK_DATABASE
ZORK_BASE_URL
ZORK_SESSION_SECRET
ZORK_TURN_TIMEOUT     # context deadline per turn
ZORK_INSTRUCTION_LIMIT
```

Support environment variables and/or command-line flags using standard-library mechanisms unless requirements grow.

Secrets must not be committed to the repository.

---

## 20. Testing Strategy

The engine has its own suite — 715 tests under `-race`, six fuzz targets, and a differential comparison against dfrotz on Zork I. Do not duplicate it. Test the host.

The project has no CI, by decision. The test gate runs when a human runs it, which makes it the committer's job rather than a machine's.

### 20.1 Game service tests

The turn cycle should be testable without HTTP or SQLite.

Test:

- loading each story and rejecting a file that is not a Version 3 story;
- keying a session to a story by SHA-256;
- the `New` → `Restore` → `Run` → persist → discard cycle;
- `Start` for the first turn of a new game;
- each error class in section 5.4.1, including that nothing is written on a failed turn;
- a `DeadlineExceeded` retry producing the turn the first attempt would have;
- a state refused with `ErrInvalidState` being reported rather than read as corruption;
- a nil `Result.State` on `Halted` not overwriting the last playable state.

### 20.2 State round-trip invariant

The key property, which the engine guarantees and this application depends on:

```text
machine A
   |
 execute commands
   |
 persist Result.State
   |
 New + Restore
   v
machine B
```

Machine B must behave equivalently to machine A from that point onward.

Assert it at this layer with a real story, so a regression in how state is stored or handed back is caught here rather than in production.

### 20.3 Rendering tests

Word wrapping, status-line presentation, upper-window handling and HTML escaping are this application's work, so they need this application's tests. Feed known `Result` values and assert the rendered output, including that the story's deliberate whitespace survives.

### 20.4 Command interception tests

`SAVE` and `RESTORE` typed by a player must reach session storage and must never reach `machine.Run`. Every other command must pass through untouched.

### 20.5 Scripted Zork tests

Add end-to-end transcripts against the real story files with a fixed seed (`zmachine.WithRandomSeed`).

For example:

```text
look
open mailbox
take leaflet
read leaflet
```

Assert expected significant output and successful arrival at the next input boundary.

Avoid tests that depend unnecessarily on exact whitespace when semantic output is sufficient — but do assert whitespace where the terminal rendering depends on it.

### 20.6 Persistence tests

Test:

- new game creation;
- command persistence;
- reload after simulated server restart;
- named save creation;
- named restore;
- restart;
- user isolation;
- stale/concurrent updates, including the conditional write in section 9;
- transaction rollback after an execution failure.

### 20.6.1 Upstream regressions

A bug that turns out to belong to the engine still gets a test here — the smallest reproducer that demonstrates it — so this project notices when the upstream fix lands. The upstream report is the story file, the exact input sequence, and the previous turn's `Result.State`.

### 20.7 HTTP tests

Use `net/http/httptest`.

Test both normal navigation and HTMX fragment behavior.

---

## 21. Implementation Milestones

### Milestone 1 — Story library

- Depend on `github.com/maloquacious/zmachine` at the pinned tag.
- Load all three `.z3` files with `LoadStory` at startup.
- Key each by SHA-256 and expose release, serial and size for diagnostics.
- Reject a file that is not a Version 3 story, with a useful error.
- Establish story provenance/licensing documentation.

### Milestone 2 — First turn

- Create a machine, call `Start`, and print the opening of each game.
- Bound the call with a context deadline and an instruction limit.
- Classify every error class in section 5.4.1.

### Milestone 3 — The turn function

Implement the whole cycle as one function with no HTTP in it:

```go
func turn(story *zmachine.Story, saved []byte, command string) (zmachine.Result, error)
```

New machine, stored state, one command, machine discarded. This is the shape everything else is built on; a request handler is this function with an HTTP request in front of it.

### Milestone 4 — Development CLI

Before building the web application, provide a CLI that drives milestone 3 in a loop:

```text
$ go run ./cmd/zorkplay zork1
> look
West of House
...
```

The story is selected by its library id rather than by a path: the images are embedded, and `games` is the only place a story file is named.

Rebuild the machine every turn even here. It separates engine and state debugging from web debugging, and exercises the production path rather than a shortcut.

### Milestone 5 — State round trip

- Persist `Result.State` between turns.
- Prove that a rebuilt-and-restored machine continues identically to one kept alive.
- Prove that a failed turn leaves the previous state intact.

This milestone is required before server-side gameplay.

### Milestone 6 — Rendering

- Word wrap to the target width without corrupting the story's whitespace.
- Render the status line from `Result.StatusLine`.
- Decide how `Result.UpperWindow` is presented.
- Escape story output on the way into HTML.

### Milestone 7 — SQLite persistence

- Integrate ZombieZen SQLite.
- Add migrations.
- Store active game state and its story key.
- Persist after every input cycle.
- Add the per-session lock and the conditional write.
- Verify restart/resume behavior.

### Milestone 8 — Authentication

- Users.
- Password hashing.
- Login/logout.
- Secure sessions.
- Authorization boundaries.

Login and logout are the operations, not the routes: `auth.Service` answers whether an email and password identify a user, and `session.Manager` turns that answer into a cookie. The handlers that call them arrive with the web terminal in milestone 9.

### Milestone 9 — Web terminal

- Server-rendered templates.
- HTMX command submission.
- Terminal transcript.
- Status line.
- Automatic focus.
- Mobile-friendly layout.

The page a browser loads and the fragment a turn returns are rendered from the same templates, so a refresh and a turn cannot disagree about what the screen looks like. htmx is served from `web/static/vendor/` rather than from a CDN: the deployment is the binary and its database, and no third party is asked for a script on every page load.

### Milestone 10 — Terminal polish

- Alpine command history.
- green/amber preference;
- optional CRT effects;
- reduced-motion handling;
- keyboard behavior.

**The decisions.** The history is browsed the way a shell browses one, is kept per game in this browser's own storage, and is bounded. The line being written is set aside when browsing starts, so arrowing up to look at something and back down returns what was half-typed. A turn that failed keeps the line so it can be sent again without being typed again. None of it is game state (section 12.3).

The phosphor and the CRT setting are attributes on the root element, written to browser storage and applied again by a small inline script in the page head — the one script that has to run before the first paint, or a player who chose amber watches the screen flash green on every page.

Reduced motion gates the one animation there is, the blinking cursor. It does not gate the glow, which is static and not something reduced motion asks about; more contrast removes both the glow and the scanlines, since taking contrast away is the one thing decoration must not do.

Typing anywhere means typing at the prompt, but only when nothing else has focus, so the transcript can still be read with the keyboard and the preference buttons still answer to the space bar. Escape clears the line.

### Milestone 11 — Named saves

- Intercept `SAVE` and `RESTORE` before the engine sees them.
- Add named saves.
- Add restore selector.
- Add deletion/overwrite semantics.
- Consider save export/import for other interpreters as a later enhancement.

The interception decision (section 13.1) is needed before milestone 9, because it shapes the terminal UI. Only the storage work waits for this milestone.

**The decisions.** The interception lives in `game.Service.Play`, not in a request handler, so there is no route to the engine that goes around it. `Play` now returns a `Turn` whose `Intent` says who answered the line: the story, or this application. The match is the first word and only the words `save` and `restore`; anything after the verb is taken as a save name, which is safe because Version 3's own save and restore can only report failure.

A bare `SAVE` or `RESTORE` is a question rather than a turn — nothing is played and nothing is written. The terminal answers it by echoing the line and swapping the command line for the field that asks for a name, or for the list to choose from. The swap is server-rendered from the same partial the page load uses, so a refresh mid-question shows what the turn showed.

A save is a whole screen, not only a state. The transcript, the status line and the move count are written beside the bytes and go back with them, because restoring bytes under a transcript from after the save point would leave the player reading about a game that is no longer there. Names are unique within a game and matched without regard to case, so saving under a name already in use replaces it — refusing would only teach players to delete first. `MaxSavesPerGame` bounds the shelf; the count and the write are one transaction.

Restoring un-halts a game: the story ended itself, and a save from before it did is the way back. That is also the only thing an ended game offers.

Save and restore forms post ordinarily rather than through htmx. Both change what a page load would produce — a restore replaces the transcript outright — so the answer is a redirect and a redraw rather than a fragment spliced into a screen that is no longer current. Deletion is a `POST` rather than a `DELETE` because a form is all a browser without JavaScript can send.

Export and import for other interpreters remain a later decision and were not built.

---

## 22. Initial Definition of Done

The first useful release is complete when:

1. A user can create or receive an account and log in.
2. The server runs the actual Zork story files through the pinned engine.
3. The user can play through the browser.
4. Game execution occurs in Go on the server, one machine per request.
5. The browser uses server-rendered HTML, HTMX, and limited Alpine.js.
6. The UI convincingly evokes an early-1980s terminal.
7. The user's state is automatically stored in SQLite after commands.
8. The user can log out, restart the Go server, log back in, and continue from the same state.
9. Different users have completely independent game states.
10. Concurrent/stale requests cannot silently overwrite newer game state.
11. An execution failure, cancellation or timeout cannot destroy the last known-good game state.
12. The application can be deployed as a Go executable with a SQLite database and minimal supporting configuration.
13. The turn cycle, rendering and persistence behavior are covered by automated tests.

---

## 23. Explicit Non-Goals for Version 1

Do not allow these to expand the initial project:

- reimplementing or forking the Z-machine engine;
- games beyond Zork I, II and III, whose compatibility is unproven and untested;
- Z-machine versions other than 3;
- arbitrary user-uploaded story files;
- save import/export or interoperability with other interpreters;
- reaching into the saved-state format for any purpose;
- multiplayer Zork;
- shared game worlds;
- SPA architecture;
- WebSockets;
- public JSON API;
- Redis;
- PostgreSQL;
- ORM adoption;
- Docker/Kubernetes as an application requirement;
- graphical maps;
- achievements;
- leaderboards;
- chat;
- social features;
- AI hints;
- general-purpose IF library/catalog support;
- exact emulation of a specific historical CRT or computer.

They may be reconsidered after the core implementation works.

---

## 24. Architectural Boundary Summary

The central boundary should remain:

```text
                 HOST APPLICATION
                       |
             +---------+---------+
             |                   |
        one line of input   Result.State
             |                   |
             v                   v
        +-----------------------------+
        |   zmachine (external pkg)   |
        |                             |
        | no HTTP                     |
        | no SQLite                   |
        | no users                    |
        | no HTML                     |
        | no filesystem               |
        | no terminal                 |
        +-----------------------------+
                       |
                       v
                games/*.z3
```

Everything above that line is this project's work — rendering, wrapping, the status bar, saving, users, transport, storage, concurrency. Everything below it is the engine's, and belongs upstream.

And externally:

```text
HTML + HTMX + Alpine
         |
         v
      net/http
         |
         v
 authentication
         |
         v
   game service
      /      \
     v        v
 zmachine   SQLite
            ZombieZen
```

Maintaining these boundaries is more important than preserving any particular directory layout or API proposed in this document.

---

## 25. Guiding Test

At any point in implementation, the project should be moving toward this scenario:

```text
User opens browser.
User logs in.

ZORK I: The Great Underground Empire

West of House
You are standing in an open field west of a white
house, with a boarded front door.

There is a small mailbox here.

> open mailbox
Opening the small mailbox reveals a leaflet.

> take leaflet
Taken.

> _
```

The user closes the browser.

The server is stopped and restarted.

The user opens the site on another device and logs in:

```text
> inventory
You are carrying:
  A leaflet

> _
```

If that works reliably, the fundamental architecture is working.
