# Zork I Web Server — Project Specification

**Status:** Implementation specification  
**Working name:** `zorkd`  
**Primary language:** Go  
**Database:** SQLite via ZombieZen (`zombiezen.com/go/sqlite`)  
**Frontend:** Server-rendered HTML + HTMX + Alpine.js  
**Game target:** Zork I running on a deliberately constrained Z-machine Version 3 interpreter

---

## 1. Purpose

Build a self-contained Go web application that runs **Zork I: The Great Underground Empire** on the server and presents it to authenticated users through a browser interface modeled after an early-1980s computer terminal.

The application owns:

- user authentication;
- per-user game state;
- the Z-machine Version 3 interpreter;
- automatic persistence after player input;
- explicit named saves and restores;
- terminal rendering state;
- static web assets.

The browser does **not** run the Z-machine. It acts as a thin terminal client.

The initial implementation is explicitly **not** a general-purpose Z-machine interpreter or interactive-fiction platform. It implements the subset of Z-machine Version 3 required to run the selected Zork I story image correctly.

---

## 2. Design Principles

### 2.1 Zork I first

Compatibility with Zork I is the primary requirement. General Z-machine compatibility is secondary.

Do not implement later Z-machine versions merely for completeness.

When a choice exists between:

1. a small, well-tested implementation sufficient for Zork I; and
2. a generalized abstraction anticipating future interpreters or games;

prefer the first unless the generalized design is equally simple.

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

The server should not require a permanently running goroutine, process, or VM instance for each player.

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
|              |                                 |
|       +------+-------+                         |
|       |              |                         |
|       v              v                         |
| Z-machine v3      persistence                  |
| interpreter       ZombieZen SQLite             |
|       |              |                         |
|       v              v                         |
| zork1.z3          zork.db                      |
+------------------------------------------------+
```

The HTTP layer must not contain Z-machine implementation details.

The Z-machine package must not know about HTTP, authentication, users, HTMX, HTML, or SQLite.

Persistence should store opaque serialized game-state data produced by the game/interpreter layer rather than reaching into VM internals.

---

## 4. Technology Choices

### 4.1 Go

Use a currently supported Go release.

Prefer the standard library where it is sufficient, particularly:

- `net/http`;
- `html/template`;
- `embed`;
- `context`;
- `log/slog`;
- `crypto/*`.

Third-party packages should have a clear purpose and should not replace simple standard-library functionality without benefit.

### 4.2 SQLite

Use:

```text
zombiezen.com/go/sqlite
```

Do **not** use `modernc.org/sqlite`.

Use SQLite in WAL mode.

Database access should be explicit and small. A large ORM is not desired.

Schema changes must be represented by versioned migrations.

### 4.3 HTMX

HTMX is the primary browser/server interaction mechanism.

Typical command submission:

```html
<form
  hx-post="/game/input"
  hx-target="#terminal"
  hx-swap="beforeend">
```

The exact markup may evolve, but the interaction model should remain HTML-over-the-wire rather than JSON API + client-side application rendering.

### 4.4 Alpine.js

Alpine.js supplements HTMX.

Appropriate uses include:

- Up/Down command history;
- restoring focus to the command line;
- toggling amber/green display;
- toggling scanlines or glow;
- small modal/menu state.

Alpine must not become a second application architecture.

### 4.5 Embedded assets

Where licensing permits, use `//go:embed` for templates and static application assets so deployment remains simple.

Whether the Zork story image itself is embedded or supplied at deployment time must be isolated behind story-loading code and documented clearly.

---

## 5. Game Runtime Model

### 5.1 Story image

The interpreter consumes a Z-machine Version 3 story image for Zork I.

The application must validate the story header before execution and reject unsupported Z-machine versions.

The story image is immutable during execution.

### 5.2 Machine creation

A conceptual API:

```go
type Machine struct {
    // unexported implementation
}

func New(story []byte, opts ...Option) (*Machine, error)
func Restore(story, snapshot []byte, opts ...Option) (*Machine, error)
```

The actual API may differ, but callers must not need to manipulate VM internals.

### 5.3 Execution

Execution proceeds until the VM reaches an event requiring the host application.

A useful conceptual model is:

```go
type Event interface {
    isEvent()
}

type Output struct {
    Text string
}

type InputRequested struct{}

type SaveRequested struct{}

type RestoreRequested struct{}

type Quit struct{}
```

The interpreter may expose a `Run`, `Step`, or event-oriented API. The important requirement is that host interaction occurs through explicit boundaries rather than callbacks into HTTP code.

### 5.4 Input lifecycle

For a normal command:

1. Authenticate the request.
2. Locate the user's active Zork game.
3. Load its persisted snapshot.
4. Restore the VM.
5. Supply the submitted line of text.
6. Execute until the next input request or other host event.
7. Capture terminal operations/output.
8. Serialize the resulting VM state.
9. Atomically persist the new state.
10. Return an HTML terminal fragment.

A failed execution must not overwrite the last known-good persisted state.

### 5.5 Automatic continuation

A player does not need to type `SAVE` merely to leave the site.

Every completed input cycle updates the active game state.

Therefore:

```text
> open mailbox
```

followed by closing the browser must be sufficient for the player to resume after `open mailbox` on the next login.

---

## 6. Z-machine Scope

### 6.1 Supported version

Initial target:

**Z-machine Version 3 only.**

The interpreter must reject unsupported story versions with a useful error.

### 6.2 Required subsystems

Implement, as required by Zork I:

- story-file header parsing;
- static and dynamic memory;
- instruction decoding;
- 0OP, 1OP, 2OP and VAR instruction forms required by the game;
- variables;
- evaluation stack;
- routine calls and call frames;
- branching;
- object tree;
- attributes;
- properties;
- dictionary access;
- Z-character/Z-string decoding;
- abbreviation tables;
- tokenization and parsing;
- random number generation;
- input;
- output;
- Version 3 screen/window behavior required by Zork;
- save;
- restore;
- restart;
- quit;
- snapshot serialization.

Unsupported instructions must fail explicitly during development rather than silently behaving incorrectly.

### 6.3 Compatibility strategy

Development should proceed from observable Zork I requirements while consulting the Z-machine specification.

A conformance test suite is valuable, but complete Z-machine conformance is not a release requirement for the first version.

Regression tests should be added whenever an opcode or VM behavior is implemented.

### 6.4 Determinism

Where possible, VM behavior should be deterministic under test.

Random-number generation should permit injection or seeding so scripted game sequences can be reproduced.

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
    email       TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE games (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    story_id    TEXT NOT NULL,
    state       BLOB NOT NULL,
    turn        INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,

    FOREIGN KEY (user_id) REFERENCES users(id)
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

### 7.3 Snapshot format

The VM package owns snapshot serialization.

Snapshots must contain everything necessary to resume execution exactly, including at minimum:

- dynamic memory;
- program counter;
- evaluation stack;
- routine/call frames;
- local variables;
- any interpreter state required for correct continuation;
- RNG state if necessary for exact continuation.

The snapshot format must contain a version identifier so it can evolve.

Do not serialize Go structs with `gob` as the long-term storage contract.

A small explicit binary format is preferred.

### 7.4 Quetzal

Standard Quetzal save-file support is desirable but is **not required for the initial milestone**.

The internal snapshot format may be simpler.

Quetzal should be considered later for import/export and interoperability with other Z-machine interpreters.

---

## 8. Authentication and Authorization

### 8.1 User identity

Users authenticate with an email address and password.

Normalize email addresses consistently for lookup.

Passwords must be stored using an appropriate password hashing algorithm. Never store plaintext passwords.

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

### 8.3 Authorization

Every game and save operation must be scoped to the authenticated user.

Never trust a game or save identifier supplied by the browser without verifying ownership.

The application must prevent one user from reading, modifying, restoring, or deleting another user's game state.

---

## 9. Concurrency

A user may accidentally submit multiple commands from multiple tabs or devices.

The application must prevent concurrent updates from corrupting or forking an active game unintentionally.

At minimum, use optimistic concurrency around a game revision/turn number or serialize updates per game.

A stale request should receive a controlled response instructing the browser to refresh the current terminal state rather than overwriting newer state.

Database transactions must preserve the last known-good state.

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

Do not send HTML from the Z-machine package.

The interpreter should produce semantic terminal operations or a similarly neutral representation.

For example:

```go
type TerminalOp interface {
    terminalOp()
}

type Write struct {
    Text string
}

type SetStatus struct {
    Text string
}

type Clear struct{}

type Prompt struct{}
```

The web layer converts these operations into HTML.

This separation prevents game output from becoming trusted HTML and makes interpreter testing independent of the browser.

### 11.1 Status line

Honor the Version 3 screen/status behavior used by Zork I.

The web UI may render the status line separately from scrolling transcript text.

### 11.2 Transcript

The server should retain enough information to reconstruct the player's useful terminal view after refresh or login.

Do not assume the VM snapshot itself contains a display transcript.

Possible approaches include:

- persisting terminal transcript separately;
- persisting a bounded recent transcript;
- regenerating an appropriate screen from a stored terminal model.

Choose the simplest implementation that produces reliable refresh/resume behavior.

A bounded transcript is preferred over unbounded database growth.

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

- preserve the previous game snapshot;
- be logged with useful context;
- return a safe user-facing error;
- never expose stack traces or secrets.

### 12.3 Alpine responsibilities

Use Alpine for browser-local command history.

Command history need not initially be synchronized across devices.

Do not confuse command history with authoritative game state.

---

## 13. Save and Restore

There are two distinct concepts.

### 13.1 Automatic state

The active game is automatically persisted after every completed command/input cycle.

This is the normal continuation mechanism.

### 13.2 Named saves

When the Z-machine requests a traditional save, the host application should present an appropriate terminal-style UI for naming the save.

Example:

```text
Save game as: before-troll
Game saved.
```

Named saves belong to the active game/user.

### 13.3 Restore

A restore request may present saved games in a terminal-oriented selector.

Example:

```text
Saved games:

1. before-troll
2. cellar
3. suspicious-grue

Restore which? _
```

The host application owns the storage UI; the Z-machine owns the semantics of the save/restore opcode result.

### 13.4 Restart

`RESTART` must restore the story to its initial state according to Version 3 semantics.

The web application may request confirmation before destructive restart, but that confirmation belongs outside the VM.

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
│   └── zorkd/
│       └── main.go
├── internal/
│   ├── auth/
│   ├── database/
│   ├── game/
│   ├── httpserver/
│   ├── session/
│   └── zmachine/
│       ├── machine.go
│       ├── header.go
│       ├── memory.go
│       ├── decode.go
│       ├── instruction.go
│       ├── op0.go
│       ├── op1.go
│       ├── op2.go
│       ├── opvar.go
│       ├── stack.go
│       ├── routine.go
│       ├── object.go
│       ├── property.go
│       ├── text.go
│       ├── dictionary.go
│       ├── random.go
│       ├── input.go
│       ├── output.go
│       ├── screen.go
│       └── snapshot.go
├── migrations/
├── web/
│   ├── static/
│   └── templates/
├── testdata/
├── go.mod
├── go.sum
└── README.md
```

Do not treat this layout as immutable. Prefer packages that correspond to real boundaries rather than creating packages merely to organize filenames.

---

## 16. Story Licensing and Provenance

The project must document exactly where its Zork I source/story image comes from and under what license it is distributed.

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
- never treat Z-machine output as HTML;
- limit command/input size;
- limit save names and validate them as data, not filesystem paths;
- use database identifiers rather than user-controlled filenames;
- authorize every game/save operation;
- set reasonable HTTP timeouts;
- limit request body sizes;
- prevent concurrent state overwrite;
- do not expose arbitrary story-file loading to ordinary users;
- do not provide filesystem access through Z-machine save/restore operations.

The Z-machine executes untrusted-ish bytecode from the perspective of the host. Memory accesses, object accesses, instruction decoding, stack operations, and story addresses must be bounds checked sufficiently to prevent malformed story data from crashing or compromising the server.

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
ZORK_STORY_FILE
```

Support environment variables and/or command-line flags using standard-library mechanisms unless requirements grow.

Secrets must not be committed to the repository.

---

## 20. Testing Strategy

### 20.1 Z-machine unit tests

The VM should be heavily testable without HTTP or SQLite.

Test:

- header parsing;
- memory rules;
- instruction decoding;
- operand decoding;
- branches;
- variables;
- stack behavior;
- calls/returns;
- objects;
- attributes;
- properties;
- Z-string decoding;
- dictionary lookup;
- tokenization;
- RNG behavior;
- snapshot/restore.

### 20.2 Snapshot invariant

A key property:

```text
machine A
   |
 execute commands
   |
 snapshot
   |
 restore
   v
machine B
```

Machine B must behave equivalently to machine A from that point onward.

Add tests that execute input, snapshot, restore into a new machine, and continue execution.

### 20.3 Scripted Zork tests

Once enough opcodes exist, add end-to-end transcripts.

For example:

```text
look
open mailbox
take leaflet
read leaflet
```

Assert expected significant output and successful arrival at the next input boundary.

Avoid tests that depend unnecessarily on exact whitespace when semantic output is sufficient.

### 20.4 Persistence tests

Test:

- new game creation;
- command persistence;
- reload after simulated server restart;
- named save creation;
- named restore;
- restart;
- user isolation;
- stale/concurrent updates;
- transaction rollback after interpreter failure.

### 20.5 HTTP tests

Use `net/http/httptest`.

Test both normal navigation and HTMX fragment behavior.

---

## 21. Implementation Milestones

### Milestone 1 — Story inspection

- Load a Zork I `.z3` file.
- Parse and validate its Version 3 header.
- Expose diagnostic information in tests or a development command.
- Establish story provenance/licensing documentation.

### Milestone 2 — Minimal VM

- Implement memory.
- Implement instruction decoding.
- Implement variables and stack.
- Implement routine calls.
- Implement enough output/text decoding to begin booting Zork.
- Fail clearly on the first unsupported opcode.

Goal:

```text
go test ./...
```

provides a steadily advancing, test-driven path through Zork startup.

### Milestone 3 — Reach the first prompt

Implement enough Version 3 behavior for Zork I to boot and produce:

```text
West of House
...
>
```

This is the first major interpreter milestone.

### Milestone 4 — Interactive CLI

Before building the web application, provide a development CLI capable of:

```text
$ go run ./cmd/zork ...
> look
West of House
...
```

This separates VM debugging from web debugging.

### Milestone 5 — Snapshot/restore

- Serialize the running VM.
- Destroy it.
- Restore a new VM from the snapshot.
- Continue playing without observable loss of state.

This milestone is required before server-side gameplay.

### Milestone 6 — SQLite persistence

- Integrate ZombieZen SQLite.
- Add migrations.
- Store active game state.
- Persist after every input cycle.
- Verify restart/resume behavior.

### Milestone 7 — Authentication

- Users.
- Password hashing.
- Login/logout.
- Secure sessions.
- Authorization boundaries.

### Milestone 8 — Web terminal

- Server-rendered templates.
- HTMX command submission.
- Terminal transcript.
- Status line.
- Automatic focus.
- Mobile-friendly layout.

### Milestone 9 — Terminal polish

- Alpine command history.
- green/amber preference;
- optional CRT effects;
- reduced-motion handling;
- keyboard behavior.

### Milestone 10 — Named saves

- Implement host handling of Version 3 save/restore.
- Add named saves.
- Add restore selector.
- Add deletion/overwrite semantics.
- Consider Quetzal export/import as a later enhancement.

---

## 22. Initial Definition of Done

The first useful release is complete when:

1. A user can create or receive an account and log in.
2. The server runs the actual Zork I Z-machine Version 3 story.
3. The user can play through the browser.
4. Game execution occurs in Go on the server.
5. The browser uses server-rendered HTML, HTMX, and limited Alpine.js.
6. The UI convincingly evokes an early-1980s terminal.
7. The user's state is automatically stored in SQLite after commands.
8. The user can log out, restart the Go server, log back in, and continue from the same state.
9. Different users have completely independent game states.
10. Concurrent/stale requests cannot silently overwrite newer game state.
11. A VM failure cannot destroy the last known-good game state.
12. The application can be deployed as a Go executable with a SQLite database and minimal supporting configuration.
13. Core VM and persistence behavior is covered by automated tests.

---

## 23. Explicit Non-Goals for Version 1

Do not allow these to expand the initial project:

- general Z-machine v4+ support;
- arbitrary user-uploaded story files;
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
          input/events       snapshots
             |                   |
             v                   v
        +-----------------------------+
        |       Z-machine v3          |
        |                             |
        | no HTTP                     |
        | no SQLite                   |
        | no users                    |
        | no HTML                     |
        | no HTMX                     |
        | no Alpine                   |
        +-----------------------------+
                       |
                       v
                    Zork I
```

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
Z-machine   SQLite
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
