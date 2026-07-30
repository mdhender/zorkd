# CLAUDE.md

## Mission

Build and maintain a small Go server that runs the original Infocom Zork trilogy as durable,
request-oriented sessions on top of the `github.com/maloquacious/zmachine` execution engine.

The supported games are:

* Zork I
* Zork II
* Zork III

The project executes their original compiled `.z3` story files.

Do not reimplement Zork's game logic.

Do not reimplement the Z-machine.

## The Engine Dependency

Z-machine Version 3 execution comes from:

```text
github.com/maloquacious/zmachine v0.2.0
```

Pin the tag. The engine is `v0.x`, its exported API has already moved once
(`ExecutionError.Op` changed type in 0.2.0), and a minor bump may break the build before
`v1.0.0`. Read the engine's `CHANGELOG.md` on every version bump.

Requires **Go 1.26 or later**. The engine imports nothing outside the standard library and
`github.com/maloquacious/quetzal`.

The engine's own documentation is authoritative for its behavior:

* `docs/tutorial.md` — loads Zork I and plays three turns, rebuilding the machine between
  each one, which is exactly the shape a request handler wants;
* `docs/how-to/` — persisting and restoring session state, handling a cancelled request
  mid-turn, serving many concurrent players from one story;
* `docs/reference.md` — lifecycle calls, options, every `Result` field, the error taxonomy,
  concurrency, and limits;
* `pkg.go.dev` — authoritative for per-symbol signatures.

Engine bugs are reported upstream at `maloquacious/zmachine`, not worked around locally.
The most useful report is the story file, the exact sequence of inputs, and the
`Result.State` from the turn before the failure. Security issues follow the engine's
`SECURITY.md` rather than its issue tracker.

Do not describe the engine as conforming to Standard 1.1 in product copy or documentation.
It implements Version 3, is written against Standard 1.1, and deliberately leaves the
header's standard revision number unset.

## Primary Design Constraint

This is **not a Z-machine project**. It is a host application.

The implementation target is:

> Reliably run Zork I–III as durable, request-oriented web sessions.

When choosing between:

1. a small implementation sufficient for Zork I–III; and
2. a more elaborate abstraction intended to support an arbitrary interactive-fiction catalog;

prefer the first unless the broader implementation makes the current code materially clearer
or safer.

Avoid speculative extensibility.

Compatibility is validated upstream on Zork I, II and III, and differentially against dfrotz
on Zork I alone. The rest of the Version 3 catalog — Planetfall, Enchanter, Hitchhiker's,
Seastalker — is unproven here. If the product ever plans a catalog beyond Zork, that testing
has to be budgeted first; it is not a small change.

## Execution Model

Execution is headless and request-oriented.

The fundamental operation is conceptually:

```text
Execute(story, state, input) -> output, newState
```

A machine lives only for the duration of one execution request.

The normal lifecycle is:

```text
load immutable *zmachine.Story (once, at startup)
        +
load the session's saved state
        │
        ▼
zmachine.New(story)
        │
        ▼
machine.Restore(saved)
        │
        ▼
machine.Run(ctx, input)
        │
        ▼
capture Result.Output / Result.StatusLine
        │
        ▼
persist Result.State
        │
        ▼
discard the machine
```

A new game calls `machine.Start(ctx)` instead of `Restore` + `Run`; it supplies no input and
runs to the first prompt. From the second turn on there is no difference between the two.

Rebuilding the machine every turn is observably identical to keeping one open — the engine's
integration tests assert exactly that. Do not cache machines between turns, do not pool them,
and do not design around a permanently running interpreter goroutine or subprocess.

## Package Boundaries

Keep these concerns separate.

### Game service

Owns the turn: choosing the story, restoring the saved state, running one command, deciding
what to persist, and classifying engine errors.

This is where `zmachine` is imported. It must not depend on HTTP.

Also owns everything the engine deliberately does not do (see **What We Own** below).

### Server

Responsible for:

* network transport;
* request validation;
* session identification;
* selecting a game;
* invoking the game service;
* constructing responses.

Do not import `zmachine` into HTTP handlers, and do not put Z-machine semantics there.

### Persistence

Responsible for durable application/session data.

Stores `Result.State` as an opaque blob. Do not make the game service depend directly on the
database implementation.

### Quetzal

`Result.State` happens to be a Quetzal snapshot, and `github.com/maloquacious/quetzal` is an
indirect dependency through the engine. Neither fact is ours to use.

Treat the state as opaque bytes:

* do not import `quetzal` directly;
* do not parse, edit, or inspect the state;
* do not recover the score or room name from it — `Result.StatusLine` reports those.

Any field recovered by hand ties this project to a save format that is free to change.

For undo or multiple save slots, keep more than one stored blob. Every state is complete and
self-contained — not a delta, not a link in a chain — so any one of them restores by itself,
and they may be deleted in any order.

## What We Own

These are host responsibilities by design, not engine gaps to wait on.

**Rendering.** The engine performs no word wrapping and has no screen width or cursor model.
`Result.Output` comes back with the story's whitespace preserved exactly. The ~80-column
terminal presentation is ours to build, and we must not corrupt the story's whitespace doing it.

**The upper window.** `Result.UpperWindow` is a separate string with no cursor positions
attached. Presenting it, and the status line, is ours.

**Status line.** `Result.StatusLine` is reported, never printed. Check `Available` before
using any other field. `Score`/`Turns` are meaningful when `TimeGame` is false; `Hours`/
`Minutes` when it is true. Zork is a score game.

**Save and restore.** See below.

**Transport, users, persistence, transactions, retries, idempotency, concurrency control.**
The engine has no filesystem, no network, no environment, and never touches process state.

## Save and Restore

In-story `SAVE` and `RESTORE` report failure without branching. That is legal Version 3
behavior and the story copes, but in Zork I the player sees `Failed.` unless the host
intercepts the command.

Since this application owns persistence, intercept `SAVE` and `RESTORE` in the game service
**before** the input reaches `machine.Run`, and wire them to session storage. The player-facing
save UI is ours to design; the engine is not involved in it.

Automatic per-turn persistence is separate from named saves and is the normal continuation
mechanism. A player never has to type `SAVE` merely to close the browser.

## Story Images

The supported story files are expected under:

```text
games/zork1/zork1-r119-880429.z3
games/zork2/zork2-r63-860811.z3
games/zork3/zork3-r25-860811.z3
```

Each name carries the release and serial number from the story's own header. A saved state
only restores against the exact story it was made from, so the identity belongs in the
filename rather than only in the bytes. Do not rename these to bare `zorkN.z3`.

Each story file has its own `LICENSE` beside it. See `games/README.md`.

They are embedded with `go:embed` by the `games` package, which is the only place a story file is named. Deployment is then the binary and its database.

Treat these bytes as immutable.

Call `zmachine.LoadStory` once per story file at startup and keep the `*Story` for the life of
the process. `LoadStory` validates the whole image and is the expensive call; `New` is cheap
because it copies only dynamic memory (11,282 bytes of Zork I's 86,838) and shares the rest.

`LoadStory` copies the image it is given, so the embedded slice is never at risk from
execution — but there is still no reason to hand it to anything that might write to it.

### Identifying a story file

Key sessions by a **SHA-256 over the story image**, taken before `LoadStory` is called.

Do not key by `Story.Release()` and `Story.Serial()`. Those identify an edition rather than a
file, and `Story.Checksum()` is 0 for early Version 3 stories that carry none. The engine's
own `IFhd` check remains its business; the hash is ours.

A stored state restores for as long as the story file is byte-identical, whatever engine
version wrote it. We never need to record which build produced a state, and never need to
migrate stored states across an engine upgrade.

## Upstream Game Assets

The Zork story files originate from the historical Infocom repositories:

```text
historicalsource/zork1
historicalsource/zork2
historicalsource/zork3
```

The compiled files come from their `COMPILED` directories.

Preserve the upstream license notices.

Do not casually copy additional historical source files into this repository. Add upstream
material only when there is a concrete reason for the project to contain it.

The ZIL source is useful as reference material but is not part of the runtime architecture.

## Go Style

Write conventional, unsurprising Go.

Prefer:

* standard-library facilities;
* small packages;
* explicit data structures;
* explicit errors;
* table-driven tests;
* deterministic tests;
* simple interfaces introduced at actual boundaries.

Avoid:

* framework-style architecture;
* dependency injection frameworks;
* unnecessary interfaces;
* Java-style object hierarchies translated into Go;
* premature generic abstractions;
* wrappers around the engine that exist only to rename its types.

An interface should generally exist because there are multiple implementations, a genuine
architectural boundary, or a testing need.

## Errors

Return errors for malformed data, invalid external state, unsupported operations, and failures
that the caller can reasonably handle.

Panics are appropriate only for genuine internal invariants whose violation indicates a
programming defect.

Malformed story files, malformed saved state, and malformed network input are inputs, not
programming defects. Return errors for them.

Errors should contain enough context to locate the failure.

Prefer:

```go
return fmt.Errorf("session %s: run turn: %w", id, err)
```

over:

```go
return err
```

### Classifying engine errors

Test for context cancellation **first**. It returns the context's own error, unwrapped, and
wraps no engine sentinel, so a `default` branch that assumes an engine error will report a
disconnected client as an interpreter bug.

```go
result, err := machine.Run(ctx, command)
switch {
case err == nil:
	// the turn happened
case errors.Is(err, context.Canceled):
	// the client went away; nobody to answer, write nothing
case errors.Is(err, context.DeadlineExceeded):
	// the turn ran out of time; the command was not played
case errors.Is(err, zmachine.ErrExecutionLimit):
	// the story exceeded its instruction budget; not transient, do not auto-retry
case errors.Is(err, zmachine.ErrInvalidState):
	// the bytes and this story do not belong together
default:
	var fault *zmachine.ExecutionError
	if errors.As(err, &fault) {
		// fault.PC, fault.Op and fault.Detail locate the instruction
	}
}
```

The other sentinels are `ErrInvalidStory`, `ErrInvalidOpcode`, `ErrMemoryAccess`, and
`ErrInvalidText`.

On any failed turn the `Result` is unusable and **nothing is written to storage**. The state
stored at the end of the previous turn is still the good one. A `DeadlineExceeded` turn may be
retried with a fresh context, the same stored state, and the same command; it produces the
turn the first attempt would have, because the random generator's state travels in the saved
state. Do not auto-retry `Canceled` or `ErrExecutionLimit`.

`Result.State` is nil when `Result.Status` is `Halted`. Do not overwrite a good state with nil
unless the session is deliberately being closed.

### Fault logging

Log `ErrExecutionFault` and `ErrInvalidOpcode` with the program counter from the first
deployment, at a level that will actually be seen.

The engine's deepest test route is roughly 46 turns plus a longer multi-seed route. The opening
and mid-game are well covered; no test finishes a game, so late-game code paths have never
executed. Silence is not evidence of correctness in territory nothing has walked yet.

## Execution Boundaries

The most important nontraditional aspect of this application is its execution boundary.

Traditional Z-machine interpreters resemble:

```text
while running:
    execute
    read terminal input
    execute
    display output
```

This project instead needs:

```text
execute
    │
    ▼
input required
    │
    ▼
return to caller
```

The engine already works this way: `Start` and `Run` each execute until the next input
boundary and return a `Result`. Do not hide that boundary behind terminal abstractions on our
side of it.

## Input and Output

Never read from stdin or write to stdout on the execution path.

Input comes from the caller. Output goes to the caller.

Story output is data, never trusted HTML. Escape it when rendering.

## Randomness

Determinism in tests comes from the engine, not from a locally invented abstraction.

Use `zmachine.WithRandomSeed(seed)` for reproducible test runs. Without it the generator is
seeded unpredictably, which is what a real game wants.

`WithFrotzRandomSeed` exists for differential comparison against Frotz and is not something
this project needs.

The generator's state travels inside `Result.State`, so a restored session continues its
sequence rather than starting a fresh one.

## Limits

Bound every turn from both ends:

```go
ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
defer cancel()

machine, err := zmachine.New(story,
	zmachine.WithLogger(logger),
	zmachine.WithInstructionLimit(5_000_000),
)
```

The deadline bounds wall-clock time; the instruction limit bounds work. Cancellation is checked
periodically rather than after every instruction, so a deadline takes effect promptly but not
instantly.

There is no way to configure an unbounded machine; a machine created without the option still
stops after ten million instructions per call.

## Concurrency

Assume multiple Zork sessions execute concurrently.

A `*Story` is immutable and safe to share across every goroutine. A `*Machine` owns all mutable
state and is **not** safe for concurrent use — one machine per in-flight request, belonging to
one goroutine for the duration of one call.

Concurrency *between* sessions is free. Concurrency *within* one session is a correctness
problem we have to solve: two turns starting from the same stored state will both succeed, both
write, and one will be lost, and the player watches a command vanish.

Take a per-session lock for the whole read-run-write cycle. Across processes, make the write
conditional on the state that was read — a version column bumped each turn, and an update that
matches on it — and reject the turn whose version has moved rather than storing it. Do not
replay it against the newer state; the player issued it against the old one.

A `*slog.Logger` passed to `WithLogger` is safe to share. A `Tracer` is not — give each machine
its own, or make it safe for concurrent calls.

## Testing Strategy

The engine has its own test suite: 715 tests under `-race`, six fuzz targets, and a
differential suite against dfrotz on Zork I. Do not duplicate it. Test the host.

### Turn-cycle tests

The core property, which the engine guarantees and we depend on:

```text
execute A
persist state
rebuild + restore
execute B
```

must agree observably with:

```text
execute A
execute B
```

Assert it at our layer with a real story so a regression in how we store or hand back state is
caught here rather than in production.

### Persistence tests

Test:

* new game creation;
* per-turn persistence;
* reload after a simulated server restart;
* named save creation and restore;
* restart;
* user isolation;
* stale/concurrent updates;
* that a failed turn leaves the previous state intact;
* that a refused state (`ErrInvalidState`) is reported rather than read as corruption.

### Rendering tests

Word wrapping, status-line presentation, upper-window handling, and HTML escaping are ours, so
they need our tests. Feed known `Result` values; assert the rendered output.

### Save/restore interception tests

`SAVE` and `RESTORE` typed by a player must reach our storage and never reach `machine.Run`.

### Story integration tests

Use the real Zork story files with a fixed seed. Feed known command sequences and verify
meaningful output and resulting behavior.

### HTTP tests

Use `net/http/httptest`. Test both normal navigation and HTMX fragment behavior.

### Regression tests

Every bug should result in a test that reproduces it before the fix. Prefer the smallest
reproducer that still demonstrates the problem.

If the bug turns out to be in the engine, the reproducer is the story file, the input sequence,
and the previous turn's `Result.State` — file it upstream and keep a test here that will notice
when it is fixed.

## Test Fixtures

Do not duplicate entire story files merely to create fixtures.

Use the embedded or canonical project copies under `games/` where integration tests require
them.

## Golden Output

Golden transcript tests are useful but should not be excessively brittle.

Normalize transport-specific or irrelevant formatting where appropriate.

Do not normalize meaningful story output merely to make a failing test pass.

When a transcript changes unexpectedly, investigate why.

## Dependencies

Before adding a dependency, ask:

> What concrete problem does this solve better than a small standard-library implementation?

Dependencies are appropriate when they provide substantial, well-tested functionality that is
outside the project's core purpose. `zmachine` is the model: it is the whole interpreter, it is
tested and fuzzed upstream, and writing it here would be the project.

Dependencies that merely save a few lines of straightforward Go are usually not worthwhile.

Run:

```text
go mod tidy
go test ./...
go vet ./...
```

after dependency changes.

## Changes

Keep changes focused.

Do not combine an engine-integration fix with unrelated refactoring unless the refactoring is
necessary for the fix.

Avoid repository-wide renaming or formatting changes as part of otherwise small work.

Preserve public APIs unless changing them is part of the requested work.

## Documentation

Exported Go identifiers should have useful Go documentation.

Document **why** where the implementation follows a surprising Z-machine or engine rule.

Avoid comments that merely translate the code into English.

Good:

```go
// SAVE is intercepted before the engine sees it: in-story save reports failure
// without branching in Version 3, and the player would just see "Failed."
```

Poor:

```go
// Check for the save command.
```

## Performance

Correctness comes first.

Zork story files are small by modern standards, and sessions execute only until the next
interaction boundary. Creating a machine per request is cheap by design.

Do not introduce complicated pooling, zero-copy machinery, unsafe code, or shared mutable memory
without measurements showing that it solves a real problem. In particular, do not cache machines
between turns as an optimization; it trades away the isolation that comes free from dropping
them.

Do not compress `Result.State` before storing it. Dynamic memory is already stored as a
run-length-compressed difference against the story file, so a second pass buys almost nothing.

Benchmark before optimizing.

## Security

Treat story files, saved state, and network input as untrusted at package boundaries.

The engine treats story files and saved states as hostile binary input and reports malformed
input as an error rather than a panic. That is its guarantee, not a reason for us to skip ours:
do not assume a stored state is valid merely because the server previously produced it.

Our own obligations:

* escape all story output when rendering HTML;
* limit command and request-body sizes;
* validate save names as data, never as filesystem paths;
* authorize every game and save operation against the authenticated user;
* set reasonable HTTP timeouts;
* never expose arbitrary story-file loading to ordinary users;
* never provide filesystem access through save/restore.

## Definition of Done

A change is complete when:

* the behavior is correct for its intended purpose;
* relevant tests exist;
* existing tests pass;
* `go test ./...` passes;
* `go vet ./...` passes;
* errors contain useful context;
* documentation reflects externally visible behavior;
* no unrelated complexity was introduced.

For changes on the execution path, test against at least one real Zork story whenever practical.

**This project has no CI, by decision.** The gate above runs only when a human runs it, so run it — `gofmt`, `go build ./...`, `go vet ./...`, `go test ./...` — before every commit. Do not propose adding CI.

## Guiding Question

When uncertain about scope, ask:

> Does this make the server better at reliably running Zork I–III as durable, request-oriented
> sessions?

If not, it probably does not belong in this project.

And its corollary:

> Is this the host's job, or the engine's?

If it is the engine's, it belongs upstream.
