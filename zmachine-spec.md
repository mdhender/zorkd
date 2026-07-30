# Headless Z3 Execution Engine Specification

**Status:** Draft
**Language:** Go
**Target:** Go 1.26 or later
**Z-machine version:** Version 3 only
**Primary workload:** Infocom Z-machine Version 3 story files
**Execution model:** Headless, request-oriented execution
**Persistence:** Quetzal save-state format

---

## 1. Purpose

This package implements a small, embeddable Z-machine Version 3 interpreter for use by server applications.

It is intentionally **not** a general-purpose interactive Z-machine interpreter.

The primary execution model is:

1. A host application provides a Z3 story.
2. The host optionally restores a previously saved Quetzal state.
3. The host supplies one line of player input.
4. The interpreter executes the story.
5. Execution stops when the story requests another line of input, terminates, or encounters an error.
6. The interpreter returns all generated output and a new Quetzal state.
7. The interpreter instance may then be discarded.

This allows a web server to treat one turn of an interactive-fiction game as a request/response operation.

Conceptually:

```text
              immutable
             story (.z3)
                 │
                 ▼
        ┌─────────────────┐
state ─▶│ Z3 Interpreter  │◀─ command
        └────────┬────────┘
                 │
            ┌────┴─────┐
            ▼          ▼
          output    new state
```

The package should make this operation simple, deterministic where the Z-machine permits it, testable, and independent of terminals or operating-system process state.

---

## 2. Goals

The package SHALL:

* implement the Z-machine Version 3 execution model required by supported V3 story files;
* operate without a terminal, TTY, curses library, or graphical interface;
* expose the interpreter as an ordinary Go package;
* accept player input programmatically;
* capture story output programmatically;
* execute until the next input boundary;
* support restoring and producing Quetzal state;
* allow many independent game sessions to be executed by the same server process;
* permit interpreter instances to be created and destroyed cheaply;
* isolate story execution from the host application;
* return errors rather than terminating the host process;
* support execution limits suitable for an Internet-facing server;
* be straightforward to test without simulated terminal I/O.

The initial implementation SHOULD prioritize correctness, simplicity, and readability over optimization.

---

## 3. Non-Goals

The package SHALL NOT initially attempt to provide:

* Z-machine Versions 1, 2, 4, 5, 6, 7, or 8;
* Glulx support;
* terminal emulation;
* ANSI output;
* curses or `tcell` integration;
* command-line user interaction;
* graphical output;
* sound;
* mouse input;
* timed input;
* Unicode extensions introduced by later Z-machine versions;
* Blorb resource handling;
* story-file discovery;
* HTTP handling;
* authentication;
* database persistence;
* player/session management;
* save-file naming or filesystem user interfaces.

A command-line interpreter MAY eventually be built on top of this package, but CLI concerns SHALL NOT be incorporated into the interpreter core.

---

## 4. Design Principle

The interpreter is a **headless, request-oriented Z3 execution engine**.

Traditional Z-machine interpreters are usually structured around a long-running interactive loop:

```text
start interpreter
      │
      ▼
display text
      │
      ▼
wait for keyboard input
      │
      ▼
execute
      │
      └───────────────┐
                      │
             repeat indefinitely
```

This package instead treats input as an execution boundary:

```text
restore state
     │
     ▼
provide command
     │
     ▼
execute instructions
     │
     ▼
story requests input
     │
     ▼
return output + state
```

The interpreter SHALL NOT block waiting for input.

When the story executes an input instruction and no further input has been supplied for the current request, control SHALL return to the host application.

---

## 5. Terminology

### Story

The immutable Z-machine story file containing executable code and initial game data.

For the initial implementation, a story MUST be a valid Version 3 story.

### Machine

An in-memory execution instance of the Z-machine.

A Machine contains mutable dynamic memory, execution state, stack frames, program counter, random-number state, and other state required by Version 3.

### Turn

One invocation of the interpreter in which the host supplies player input and the interpreter executes until another input request, termination, or failure.

A Turn does not necessarily correspond to the story's internal concept of a game turn.

### Input Boundary

A point where the story executes the Version 3 line-input instruction and requires input that the host has not supplied.

### State

All information necessary to resume execution of the story.

Persisted state SHALL use Quetzal.

### Host

The application embedding this package, typically a web server.

---

## 6. Package Architecture

The implementation SHOULD separate the following concerns:

```text
z3
├── machine
│   ├── execution
│   ├── instructions
│   ├── variables
│   ├── stack
│   └── routines
│
├── memory
│
├── object
│
├── text
│   ├── zstring
│   ├── zscii
│   ├── abbreviations
│   └── dictionary
│
├── story
│
└── state
    └── Quetzal adapter
```

These MAY be Go packages or internal source-file groupings.

The implementation SHOULD avoid creating packages solely for organizational purposes when ordinary Go files provide sufficient separation.

---

## 7. Public API

The public API SHOULD remain deliberately small.

A representative API is:

```go
package z3

type Story struct {
    // unexported
}

func LoadStory(data []byte) (*Story, error)

type Machine struct {
    // unexported
}

func New(story *Story, opts ...Option) (*Machine, error)

func (m *Machine) Restore(data []byte) error

func (m *Machine) Run(input string) (Result, error)
```

`Story` SHOULD represent validated immutable story data.

Multiple Machine instances MAY share a Story safely.

This allows a server to load the `.z3` story once:

```go
story, err := z3.LoadStory(data)
```

and subsequently create short-lived machines for requests:

```go
machine, err := z3.New(story)
```

The implementation SHOULD NOT copy immutable story memory unnecessarily for every request.

---

## 8. Result

Execution SHALL return a structured result.

For example:

```go
type Result struct {
    Output string
    State  []byte
    Status Status
}
```

Possible statuses SHOULD include at least:

```go
type Status uint8

const (
    WaitingForInput Status = iota
    Halted
)
```

`WaitingForInput` means execution successfully reached another input boundary.

`Halted` means the story intentionally terminated execution.

Interpreter faults SHALL be returned as errors rather than encoded as a normal status.

---

## 9. Request Lifecycle

A typical request against an existing game SHALL be:

```go
machine, err := z3.New(story)
if err != nil {
    return err
}

if err := machine.Restore(savedState); err != nil {
    return err
}

result, err := machine.Run(command)
if err != nil {
    return err
}

save(result.State)
send(result.Output)
```

The Machine MAY be discarded immediately afterward.

The server SHALL NOT be required to retain a Machine between requests.

---

## 10. Starting a New Game

A new story has an important difference from an existing session: it may execute for some time before requesting its first command.

The API SHALL support execution without initially supplying player input.

This MAY be represented as:

```go
result, err := machine.Start()
```

or through a generalized execution method.

The preferred explicit API is:

```go
func (m *Machine) Start() (Result, error)
func (m *Machine) Run(input string) (Result, error)
```

`Start` SHALL:

1. begin execution at the story's initial program counter;
2. collect initial output;
3. execute until the first input boundary or termination;
4. return the initial output;
5. return Quetzal state representing the machine at that boundary.

For Zork I, this corresponds conceptually to returning the opening text and initial prompt before the player has entered the first command.

---

## 11. Input Semantics

`Run` SHALL provide exactly one line of host input to the story.

The implementation SHALL perform the transformations required by Version 3 before placing input into the story's text buffer.

The story's dictionary and parser machinery SHALL remain responsible for interpreting commands.

The host SHALL NOT tokenize or interpret Zork commands.

For example:

```go
result, err := machine.Run("open mailbox")
```

does not invoke an `Open` operation in the interpreter.

It supplies character input to the Z-machine exactly as an interactive interpreter would.

Once that supplied line has been consumed, a subsequent request for line input SHALL cause execution to stop and return `WaitingForInput`.

---

## 12. Output Semantics

All textual output produced during execution SHALL be captured.

The interpreter SHALL NOT write story output directly to:

* `os.Stdout`;
* `os.Stderr`;
* a terminal;
* a network connection;
* a log.

The host receives output through `Result.Output`.

Output SHALL preserve meaningful whitespace produced by the story.

Terminal-specific presentation SHALL not be included.

---

## 13. Screen Model

Version 3 contains screen-oriented operations despite this package being headless.

The package SHALL implement the minimum logical screen semantics required by Version 3.

Screen operations SHALL update interpreter state or captured output as appropriate but SHALL NOT require a physical display.

The status line SHALL be represented separately from normal story output when necessary.

The Result type MAY therefore eventually include:

```go
type Result struct {
    Output string
    StatusLine StatusLine
    State  []byte
    Status Status
}
```

The exact public representation SHOULD be driven by actual Version 3 requirements rather than terminal conventions.

---

## 14. Story Memory

The implementation SHALL enforce the Version 3 memory model.

Story memory SHALL distinguish between:

* dynamic memory;
* static memory;
* high memory.

Writes outside regions writable under the Version 3 specification SHALL fail with an interpreter error.

Immutable story data SHOULD be shared between Machine instances.

A Machine SHOULD allocate only the mutable state required for its execution.

---

## 15. Instruction Decoder

The decoder SHALL support Version 3 instruction forms and operand encodings.

Instruction decoding SHALL be separate from instruction execution.

A decoded instruction SHOULD contain sufficient information to represent:

* opcode;
* operands;
* operand types;
* result variable, when present;
* branch information, when present;
* embedded text, when present;
* address of the instruction.

Malformed instructions SHALL return descriptive errors.

The decoder SHALL NOT panic because of malformed story data.

---

## 16. Variables and Stack

The interpreter SHALL implement Version 3 variable semantics:

* variable 0 as the evaluation stack;
* local variables;
* global variables.

Routine calls SHALL create appropriate stack frames containing at least:

* return address;
* local variables;
* evaluation stack state;
* result destination where applicable.

Routine return SHALL restore the calling frame according to Version 3 semantics.

---

## 17. Arithmetic

Arithmetic operations SHALL implement Z-machine 16-bit semantics.

Signed operations SHALL interpret values as signed 16-bit integers where required.

Unsigned operations SHALL preserve unsigned 16-bit behavior where required.

Overflow SHALL behave according to Z-machine semantics rather than Go's native `int` semantics.

Internal helper functions SHOULD make signed/unsigned conversions explicit.

---

## 18. Objects

The interpreter SHALL implement the Version 3 object model, including:

* object relationships;
* parent;
* sibling;
* child;
* attributes;
* property tables;
* property defaults;
* insertion;
* removal.

Object operations SHOULD be isolated from opcode handlers behind well-tested helper functions.

Malformed object references SHALL produce interpreter errors rather than unsafe memory access.

---

## 19. Text

The package SHALL implement Version 3 text handling required by supported stories, including:

* Z-characters;
* ZSCII;
* alphabet shifts;
* abbreviations;
* packed strings;
* dictionary words;
* tokenization.

Text decoding SHOULD be independently testable without executing a Machine.

---

## 20. Dictionary and Parsing

The interpreter SHALL implement the Version 3 dictionary mechanisms used by the `read` instruction.

The interpreter is responsible for the Z-machine-level mechanics of:

* storing input;
* normalizing input as required by Version 3;
* tokenizing input;
* dictionary lookup;
* populating the parse buffer.

The interpreter SHALL NOT implement game-specific grammar.

Grammar and command interpretation remain part of the story.

---

## 21. Random Numbers

The Version 3 random-number opcode SHALL be supported.

The Machine SHOULD accept an optional random-number source or seed for testing.

For example:

```go
machine, err := z3.New(
    story,
    z3.WithRandomSeed(42),
)
```

Tests SHOULD be able to produce deterministic execution.

Production execution MAY use an automatically generated seed.

Random-number state required to resume the game correctly SHALL be included in persisted state where required by the selected Quetzal implementation and interpreter semantics.

---

## 22. Quetzal Persistence

The project assumes an existing independent Go package implementing the Quetzal specification.

This package SHALL use that implementation rather than implement the Quetzal binary format internally.

The Z3 package SHALL be responsible for translating between:

```text
Machine
   │
   ▼
Z-machine state representation
   │
   ▼
Quetzal package
   │
   ▼
[]byte
```

and the reverse operation.

The adapter SHALL preserve all execution state required to resume a Version 3 story correctly.

The core execution engine SHOULD NOT depend on Quetzal chunk encoding details.

---

## 23. Automatic Snapshotting

When execution reaches an input boundary, `Run` SHALL produce a Quetzal snapshot representing the state from which execution should resume when the next command is supplied.

The resulting state MUST correspond to a well-defined suspension point.

The host SHOULD be able to:

1. discard the Machine;
2. create another Machine;
3. restore the returned state;
4. supply another command;

with behavior indistinguishable from retaining the original Machine continuously.

This property SHALL have integration tests.

---

## 24. Story SAVE and RESTORE Opcodes

Version 3 story-level `save` and `restore` opcodes SHALL be treated separately from the host's automatic per-request persistence.

The initial implementation MAY define a host policy for these operations.

The interpreter core SHOULD expose these operations to the host rather than assume filesystem interaction.

For example, execution MAY return:

```go
type HostRequest struct {
    Kind HostRequestKind
}
```

with operations such as:

```go
SaveRequested
RestoreRequested
```

Alternatively, the initial server integration MAY explicitly report these operations as unavailable according to valid Version 3 opcode semantics.

The interpreter SHALL NOT:

* prompt for filenames;
* access the filesystem;
* assume an interactive terminal.

The exact user-facing behavior of `SAVE` and `RESTORE` belongs to the host application.

---

## 25. Execution Limits

Because story code executes inside a server process, the interpreter MUST protect the host from runaway execution.

The Machine SHALL support an instruction limit per invocation.

For example:

```go
z3.WithInstructionLimit(1_000_000)
```

Exceeding the limit SHALL return a distinguishable error.

The package SHOULD also support `context.Context` cancellation:

```go
func (m *Machine) Run(
    ctx context.Context,
    input string,
) (Result, error)
```

The implementation need not check the context after every instruction, but cancellation latency SHOULD remain small.

No story program SHALL be able to monopolize a server worker indefinitely.

---

## 26. Resource Safety

The interpreter SHALL treat story files and saved states as untrusted input.

It MUST validate offsets and bounds before memory access.

Malformed data SHALL NOT cause:

* process termination;
* arbitrary memory access;
* uncontrolled allocation;
* unbounded recursion;
* panic under ordinary malformed-input conditions.

Panics MAY remain appropriate for violations of internal invariants that indicate interpreter bugs.

Public entry points SHOULD convert unsafe external-input conditions into ordinary errors.

---

## 27. No Process-Level Side Effects

The interpreter package SHALL NOT call:

```go
os.Exit(...)
```

under any circumstances.

It SHALL NOT install signal handlers.

It SHALL NOT modify process-global terminal state.

It SHALL NOT depend on the current working directory.

It SHALL NOT read environment variables as part of normal VM execution.

It SHALL NOT implicitly open story or save files.

Termination of a Z-machine story SHALL return `Halted` to the host.

---

## 28. Concurrency

A `Story` SHALL be safe for concurrent use by multiple Machine instances.

A `Machine` need not be safe for concurrent use.

The intended pattern is:

```text
                  ┌─ Machine A ─ player A
                  │
Shared Story ─────┼─ Machine B ─ player B
                  │
                  ├─ Machine C ─ player C
                  │
                  └─ Machine D ─ player D
```

No mutable VM state SHALL be stored in package-level variables.

Multiple players executing the same story concurrently SHALL have completely isolated game states.

---

## 29. Error Model

Errors SHOULD distinguish major classes of failure.

For example:

```go
var (
    ErrInvalidStory     = errors.New("invalid Z3 story")
    ErrInvalidState     = errors.New("invalid saved state")
    ErrInvalidOpcode    = errors.New("invalid opcode")
    ErrMemoryAccess     = errors.New("invalid memory access")
    ErrExecutionLimit   = errors.New("execution limit exceeded")
)
```

Errors SHOULD contain useful context.

For example:

```text
z3: opcode 0x1a at pc 0x4f32: write outside dynamic memory
```

The package SHOULD support `errors.Is` and `errors.As` where callers may reasonably need to classify failures.

---

## 30. Logging and Diagnostics

The core interpreter SHALL NOT require a logger.

Optional tracing MAY be provided.

Tracing SHOULD be injectable, for example:

```go
type Tracer interface {
    Instruction(TraceInstruction)
}
```

Tracing MUST be disabled by default.

A tracing implementation MUST NOT change execution semantics.

Tracing SHOULD make it possible to inspect:

* program counter;
* opcode;
* operands;
* call depth;
* branch result;
* writes to variables;
* routine calls and returns.

This will be particularly valuable when comparing behavior against an established Z-machine interpreter.

---

## 31. Testing Strategy

Testing SHALL occur at several levels.

### Unit Tests

Unit tests SHOULD cover independently:

* memory access;
* instruction decoding;
* operand decoding;
* signed arithmetic;
* stack operations;
* routine calls;
* variables;
* branches;
* objects;
* properties;
* Z-string decoding;
* abbreviations;
* dictionary lookup;
* tokenization.

### Opcode Tests

Individual opcode handlers SHOULD be testable using small constructed Machine states.

### Story Tests

Small purpose-built Version 3 story files SHOULD be used where practical.

Established Z-machine conformance tests SHOULD be incorporated when licensing and format permit.

### Integration Tests

At least one integration test SHALL execute a known Version 3 story through multiple request boundaries.

Conceptually:

```go
start := Start()

assert.Contains(start.Output, openingText)

turn1 := RestoreAndRun(start.State, "look")
assert.Contains(turn1.Output, expectedLookText)

turn2 := RestoreAndRun(turn1.State, "inventory")
assert.Contains(turn2.Output, expectedInventoryText)
```

The Machine instance SHALL be destroyed and recreated between turns.

This verifies the central architectural promise of the package.

---

## 32. Zork I Compatibility Testing

Zork I SHALL serve as the primary initial real-world compatibility target.

Tests SHOULD include representative operations involving:

* movement;
* object examination;
* taking and dropping objects;
* containers;
* inventory;
* darkness;
* combat;
* random behavior;
* scoring;
* death;
* restart;
* game termination.

Tests SHOULD deliberately exercise execution across many Quetzal restore boundaries.

The test suite SHOULD verify that request-oriented execution produces the same observable game behavior as continuous execution.

Zork I compatibility SHALL NOT justify hard-coding Zork-specific behavior into the interpreter.

---

## 33. Differential Testing

Where practical, interpreter behavior SHOULD be compared with a mature reference Z-machine implementation.

Given:

```text
same story
same starting state
same commands
same deterministic RNG conditions
```

the observable story output and resulting execution state SHOULD agree.

Differential testing is particularly valuable for:

* instruction decoding;
* signed comparisons;
* branches;
* object properties;
* packed addresses;
* text decoding;
* dictionary parsing.

---

## 34. Fuzz Testing

Go fuzz tests SHOULD target parsers and decoders that consume untrusted binary structures.

High-value fuzz targets include:

```text
story header
instruction decoder
Z-string decoder
object/property tables
dictionary
Quetzal restore adapter
```

The primary fuzz invariant is:

> Arbitrary input must not crash the process.

Additional invariants SHOULD be added where practical.

---

## 35. Performance

Performance is secondary to correctness.

Nevertheless, the server execution model creates several useful requirements.

Loading and validating a Story SHOULD be relatively expensive compared with creating a Machine.

The intended server lifecycle is:

```go
// server startup
story := LoadStory(zork)

// request 1
machine := New(story)
machine.Restore(state1)
result1 := machine.Run(command1)

// request 2
machine := New(story)
machine.Restore(state2)
result2 := machine.Run(command2)
```

The server SHOULD NOT need to reread or revalidate the story file on every request.

Creating a Machine SHOULD avoid copying static and high memory.

Dynamic memory will necessarily be private to each Machine.

No optimization that complicates correctness SHOULD be introduced without measurement.

---

## 36. Server Integration Boundary

This package SHALL have no knowledge of HTTP.

A web handler might eventually use it as follows:

```go
func play(
    ctx context.Context,
    story *z3.Story,
    saved []byte,
    command string,
) (z3.Result, error) {
    machine, err := z3.New(story)
    if err != nil {
        return z3.Result{}, err
    }

    if len(saved) != 0 {
        if err := machine.Restore(saved); err != nil {
            return z3.Result{}, err
        }
    }

    return machine.Run(ctx, command)
}
```

Everything outside this boundary belongs to the application.

This includes:

* identifying the player;
* finding the game;
* loading state from storage;
* transactions;
* authorization;
* request validation;
* storing the returned Quetzal state;
* formatting JSON;
* WebSocket or HTTP transport.

---

## 37. Transactional Server Semantics

The design SHOULD permit the host to treat a turn transactionally:

```text
BEGIN

load saved Quetzal
        │
        ▼
execute command
        │
        ▼
new Quetzal + output
        │
        ▼
store new Quetzal

COMMIT
```

If execution fails, the previously stored Quetzal state remains authoritative.

The interpreter SHALL NOT mutate external persistence itself.

This allows the server to prevent partially executed turns from corrupting player state.

---

## 38. Request Idempotency

The interpreter itself SHALL NOT attempt to provide request idempotency.

Given identical:

* story;
* Quetzal state;
* input;
* random state;

execution SHOULD produce identical results where the Z-machine semantics permit deterministic reproduction.

HTTP-level request IDs, duplicate-command detection, retries, and transactional idempotency belong to the host application.

---

## 39. Proposed Repository Layout

A reasonable initial layout is:

```text
.
├── LICENSE
├── README.md
├── SPEC.md
├── go.mod
├── story.go
├── machine.go
├── execute.go
├── decode.go
├── opcode.go
├── opcode_0op.go
├── opcode_1op.go
├── opcode_2op.go
├── opcode_var.go
├── memory.go
├── variable.go
├── stack.go
├── routine.go
├── object.go
├── property.go
├── text.go
├── zscii.go
├── dictionary.go
├── input.go
├── output.go
├── state.go
├── errors.go
├── trace.go
└── internal/
    └── teststory/
```

The project SHOULD begin flat.

Subpackages SHOULD be introduced only when a clear API or dependency boundary emerges.

---

## 40. Implementation Strategy

The implementation is expected to be derived from an existing Version 3-capable Go Z-machine implementation, initially `Drakmyth/golang-zmachine`.

The project SHOULD treat that implementation as a source of tested VM machinery rather than preserve its architecture.

Useful components MAY be adapted for:

* memory;
* instruction decoding;
* stack frames;
* variables;
* objects;
* properties;
* Z-string handling;
* dictionary handling;
* Version 3 opcode implementations.

Terminal-oriented architecture SHOULD be removed rather than wrapped.

In particular, the new implementation SHOULD NOT preserve abstractions whose primary purpose is supporting `tcell`, terminal lifecycle, command-line operation, or blocking keyboard input.

The result should look and behave like a purpose-built server execution engine rather than a terminal interpreter hidden behind an adapter.

---

## 41. Dependency Policy

The core package SHOULD have very few dependencies.

It SHALL depend on the project's Quetzal package for standardized save-state encoding and decoding.

It SHOULD otherwise prefer the Go standard library.

In particular, the interpreter SHALL NOT require:

* Cobra;
* `tcell`;
* curses bindings;
* CGo;
* database drivers;
* HTTP frameworks.

The package SHOULD remain usable in an ordinary pure-Go server binary.

---

## 42. Compatibility Philosophy

The implementation target is:

> Correct Z-machine Version 3 behavior sufficient to run ordinary Version 3 stories, with Zork I as the primary compatibility workload.

This is intentionally different from:

> Implement only the exact instructions observed while playing Zork I.

The latter creates hidden compatibility holes and makes correctness difficult to reason about.

The implementation SHOULD therefore implement the defined Version 3 instruction and machine surface systematically.

Features exclusive to later Z-machine versions SHALL be omitted.

---

## 43. Definition of Done — Initial Milestone

The first major milestone is complete when the following sequence works entirely in-process:

```text
load Zork I story once
        │
        ▼
create Machine
        │
        ▼
Start()
        │
        ▼
receive opening output + Quetzal
        │
        ▼
destroy Machine
        │
        ▼
create new Machine
        │
        ▼
restore Quetzal
        │
        ▼
Run("open mailbox")
        │
        ▼
receive correct output + new Quetzal
        │
        ▼
destroy Machine
        │
        ▼
repeat
```

No terminal SHALL be involved.

No Machine SHALL need to survive between commands.

The story SHALL remain loaded and reusable across requests.

A sequence of ordinary Zork I commands SHALL produce behavior equivalent to playing the same commands through a conventional compliant interpreter.

---

## 44. Definition of Done — Server-Ready Milestone

The package is considered ready for integration into the web server when:

* Zork I can be played through repeated create/restore/run/destroy cycles;
* Quetzal state survives arbitrary request boundaries;
* concurrent players have isolated state;
* malformed state cannot crash the server;
* execution limits prevent runaway story execution;
* context cancellation works;
* no terminal dependencies remain;
* no interpreter path calls `os.Exit`;
* core Version 3 behavior has unit tests;
* request-boundary behavior has integration tests;
* important binary parsers have fuzz tests;
* errors contain enough context to diagnose interpreter faults;
* the package passes `go test -race`;
* the package passes `go vet`;
* the public API is documented.

---

## 45. Guiding Constraint

When implementation choices are ambiguous, prefer the design that best preserves this invariant:

> **The Z3 interpreter is a deterministic, headless execution engine invoked by a host application to advance a saved story from one input boundary to the next.**

The interpreter owns Z-machine execution.

The Quetzal package owns portable state representation.

The web server owns players, persistence, transactions, security, and transport.

Keeping those boundaries explicit is the central architectural goal of this package.
