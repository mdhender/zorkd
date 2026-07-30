# CLAUDE.md

## Mission

Build and maintain a small Go server capable of running the original Infocom Zork trilogy through a headless, request-oriented Version 3 Z-machine execution engine.

The supported games are:

* Zork I
* Zork II
* Zork III

The project executes their original compiled `.z3` story files.

Do not reimplement Zork's game logic.

## Primary Design Constraint

This is **not a general-purpose Z-machine project**.

The implementation target is:

> Correctly execute the Z-machine Version 3 behavior required by the supported Zork story files.

When choosing between:

1. a small implementation sufficient for Zork I–III; and
2. a more elaborate abstraction intended to support arbitrary Z-machine versions;

prefer the first unless the broader implementation makes the current code materially clearer or safer.

Avoid speculative extensibility.

## Execution Model

The Z-machine engine is headless and request-oriented.

The fundamental operation is conceptually:

```text
Execute(story, state, input) -> output, newState
```

The exact Go API may differ, but preserve this model.

A VM instance should normally live only for the duration of an execution request.

The normal lifecycle is:

```text
load immutable story image
        +
load mutable session state
        │
        ▼
instantiate VM
        │
        ▼
provide player input
        │
        ▼
execute until next input boundary
        │
        ▼
capture output
        │
        ▼
serialize state
        │
        ▼
discard VM
```

Do not design around a permanently running interpreter goroutine or subprocess.

## Package Boundaries

Keep these concerns separate.

### Z3 execution

Responsible for:

* story-file parsing;
* VM memory;
* instruction decoding;
* instruction execution;
* stack and call frames;
* objects and properties;
* Z-machine text;
* input;
* output;
* execution boundaries.

It must not depend on HTTP.

### Quetzal

Responsible for encoding and decoding persistent Quetzal save state.

Keep Quetzal functionality sufficiently independent that it can be tested without the web server.

### Server

Responsible for:

* network transport;
* request validation;
* session identification;
* selecting a game;
* loading state;
* invoking the execution engine;
* storing resulting state;
* constructing responses.

Do not put Z-machine instruction semantics in HTTP handlers.

### Persistence

Responsible for durable application/session data.

Do not make the execution engine depend directly on the database or persistence implementation.

## Story Images

The supported story files are expected under:

```text
games/zork1/zork1.z3
games/zork2/zork2.z3
games/zork3/zork3.z3
```

They may be embedded with `go:embed`.

Treat these bytes as immutable.

Never allow execution to modify the shared embedded byte slice.

Each VM must have its own mutable dynamic-memory state.

Static and high memory may be shared only when doing so is demonstrably safe under Z-machine semantics and Go concurrency.

Correctness is more important than avoiding a small memory copy.

## Upstream Game Assets

The Zork story files originate from the historical Infocom repositories:

```text
historicalsource/zork1
historicalsource/zork2
historicalsource/zork3
```

The compiled files come from their `COMPILED` directories.

Preserve the upstream license notices.

Do not casually copy additional historical source files into this repository. Add upstream material only when there is a concrete reason for the project to contain it.

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
* abstractions introduced solely for hypothetical Z4–Z8 support.

An interface should generally exist because there are multiple implementations, a genuine architectural boundary, or a testing need.

## Errors

Return errors for malformed data, invalid external state, unsupported operations, and failures that the caller can reasonably handle.

Panics are appropriate only for genuine internal invariants whose violation indicates a programming defect.

Malformed story files and malformed Quetzal data are inputs, not programming defects. Return errors for them.

Errors should contain enough context to locate the failure.

Prefer:

```go
return fmt.Errorf("decode instruction at %#x: %w", pc, err)
```

over:

```go
return err
```

## Z-machine Specification

Implement Version 3 semantics accurately.

Do not infer instruction behavior solely from observed Zork behavior when authoritative Z-machine documentation is available.

At the same time, do not implement unrelated later-version behavior merely because it appears beside the Version 3 behavior in the specification.

When specification behavior and observed game behavior appear to disagree:

1. preserve the failing case as a test;
2. verify the story-file version and instruction encoding;
3. verify the applicable Version 3 rule;
4. investigate interpreter quirks only after those checks.

Document intentional compatibility quirks.

## Execution Boundaries

The most important nontraditional aspect of this interpreter is its execution boundary.

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

The engine must be able to stop cleanly when the game requires external input.

It must return enough state for the application to resume execution later as though execution had remained continuous.

Do not hide this boundary behind terminal abstractions.

## Input and Output

The engine must not read directly from stdin or write directly to stdout.

Input comes from the caller.

Output goes to the caller.

This is mandatory for server operation and makes the engine independently testable.

## Randomness

Z-machine randomness must be isolated behind a mechanism that allows deterministic testing.

Tests must be able to control random results or seed behavior where the specification permits it.

Do not use uncontrolled package-global randomness.

## Concurrency

Assume multiple Zork sessions may execute concurrently.

Shared story images are immutable.

Mutable VM state is session-local.

Avoid global mutable interpreter state.

Running one session must never affect another session.

Use synchronization only where state is genuinely shared. Do not add locks around objects that naturally belong to a single execution.

## Quetzal

Quetzal is the durable representation of game execution state.

Saving and restoring must preserve everything necessary for execution to continue correctly.

Round-trip tests are required:

```text
state
  │
  ▼
encode Quetzal
  │
  ▼
decode Quetzal
  │
  ▼
equivalent state
```

Also test:

```text
execute A
save
restore
execute B
```

against:

```text
execute A
execute B
```

The observable results should agree.

## Testing Strategy

Use several levels of testing.

### Unit tests

Test isolated mechanics such as:

* instruction decoding;
* packed addresses;
* variable access;
* stack operations;
* branching;
* routine calls;
* object relationships;
* properties;
* ZSCII/Z-string decoding;
* memory boundaries.

### Instruction tests

Construct minimal VM states and execute individual instructions.

Test both normal and boundary cases.

### Save tests

Test Quetzal encoding, decoding, corruption handling, and round trips.

### Story integration tests

Use the real Zork story files.

Feed known command sequences and verify meaningful output and resulting behavior.

These tests are especially important because they validate interactions among many individually correct VM components.

### Regression tests

Every interpreter bug should result in a test that reproduces the bug before the fix.

Prefer the smallest reproducer that still demonstrates the problem.

## Test Fixtures

Do not duplicate entire story files merely to create fixtures.

Use the embedded or canonical project copies where integration tests require them.

For unit tests, prefer tiny synthetic memory images containing only the structures needed by the test.

## Golden Output

Golden transcript tests are useful but should not be excessively brittle.

Normalize transport-specific or irrelevant formatting where appropriate.

Do not normalize meaningful Z-machine output merely to make a failing test pass.

When a transcript changes unexpectedly, investigate why.

## Dependencies

Before adding a dependency, ask:

> What concrete problem does this solve better than a small standard-library implementation?

Dependencies are appropriate when they provide substantial, well-tested functionality that is outside the project's core purpose.

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

Do not combine an interpreter fix with unrelated refactoring unless the refactoring is necessary for the fix.

Avoid repository-wide renaming or formatting changes as part of otherwise small work.

Preserve public APIs unless changing them is part of the requested work.

## Documentation

Exported Go identifiers should have useful Go documentation.

Document **why** where the implementation follows a surprising Z-machine rule.

Avoid comments that merely translate the code into English.

Good:

```go
// Dynamic memory is copied for every VM because Version 3 games may
// modify any byte below the static-memory base.
```

Poor:

```go
// Copy the memory.
```

## Performance

Correctness comes first.

Zork story files are small by modern standards, and sessions execute only until the next interaction boundary.

Do not introduce complicated pooling, zero-copy machinery, unsafe code, or shared mutable memory without measurements showing that it solves a real problem.

Benchmark before optimizing.

## Security

Treat story files, Quetzal saves, and network input as untrusted at package boundaries.

Validate offsets and lengths before indexing memory.

Malformed VM data must not permit out-of-bounds access.

Do not assume a save file is valid merely because the server previously produced it; persisted data can become corrupted.

Fuzzing parsers and instruction decoding is encouraged.

## Definition of Done

A change is complete when:

* the implementation is correct for its intended Version 3 behavior;
* relevant tests exist;
* existing tests pass;
* `go test ./...` passes;
* `go vet ./...` passes;
* errors contain useful context;
* documentation reflects externally visible behavior;
* no unrelated complexity was introduced.

For interpreter changes, test against at least one real Zork story whenever practical.

## Guiding Question

When uncertain about scope, ask:

> Does this make the server better at reliably running Zork I–III as durable, request-oriented sessions?

If not, it probably does not belong in this project.
