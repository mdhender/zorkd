package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maloquacious/zmachine"
)

// Errors a caller is expected to handle.
var (
	// ErrSessionNotFound means no session exists under that identifier.
	ErrSessionNotFound = errors.New("game: session not found")

	// ErrVersionConflict means the stored session moved on between the read
	// and the write: another turn got there first. The turn that lost is
	// refused rather than replayed, because the player issued it against the
	// state they could see.
	ErrVersionConflict = errors.New("game: session changed since it was read")

	// ErrGameOver means the story ended itself and there is nothing left to
	// resume. Start a new game.
	ErrGameOver = errors.New("game: the story has ended")

	// ErrStoryUnavailable means the session belongs to a story this binary
	// does not carry. It is a deployment problem, not a damaged session.
	ErrStoryUnavailable = errors.New("game: story not in this library")
)

// A Session is one game in progress: which story it belongs to, the bytes that
// resume it, and enough bookkeeping to detect a lost update.
//
// State is opaque. Nothing here parses it, and the score and room come from
// [zmachine.Result.StatusLine] rather than from these bytes.
type Session struct {
	ID string

	// UserID owns the session. Every read and every write is scoped to it, so
	// an identifier a browser supplies can only reach that browser's games.
	UserID string

	// StoryKey identifies the exact story image State was written from. A
	// state only restores into a machine built from that file.
	StoryKey StoryKey

	// State resumes the session. It is nil once Halted is true, which is the
	// one time nil is a legitimate thing to store.
	State []byte

	// Transcript is what the player has seen, as the story wrote it, with the
	// player's own lines interleaved where the terminal showed them.
	//
	// It is kept because the saved state has no transcript in it and nothing
	// in it may be parsed to recover one: without this, a browser that
	// refreshes has nothing to redraw. It is bounded — see
	// [MaxTranscriptBytes] — so a long game does not become a large row.
	Transcript string

	// Status is the status bar the story last reported, kept for the same
	// reason as the transcript: a browser that refreshes must be able to draw
	// the whole screen without playing a turn to produce it again.
	Status StatusLine

	// Turn counts the commands this application has played, which is not the
	// story's own move counter.
	Turn int

	// Version is bumped by every successful write and is what makes a
	// conditional update possible. A caller does not set it.
	Version int64

	// Halted records that the story ended itself. A halted session cannot be
	// played again.
	Halted bool
}

// A Store holds sessions durably.
//
// It stores State as opaque bytes: it must not compress it, parse it, or write
// anything derived from it. Implementations may be used from several goroutines
// at once.
//
// Every lookup is scoped to the owning user. A session that belongs to somebody
// else is reported as ErrSessionNotFound rather than as a refusal, because
// distinguishing the two would let a stranger count the games on the server.
type Store interface {
	// Create stores a new session and returns it with ID and Version
	// assigned by the store. session.UserID must be set.
	Create(ctx context.Context, session Session) (Session, error)

	// Load returns the user's session, or ErrSessionNotFound.
	Load(ctx context.Context, userID, id string) (Session, error)

	// Update writes the session only if it still belongs to session.UserID and
	// the stored version still matches session.Version, and returns it with
	// the new version. It returns ErrVersionConflict if the stored version has
	// moved, and ErrSessionNotFound if the session is gone.
	Update(ctx context.Context, session Session) (Session, error)

	// List returns the user's games, most recently played first.
	List(ctx context.Context, userID string) ([]Summary, error)

	// Delete removes the user's game and every save it holds, or returns
	// ErrSessionNotFound.
	//
	// The saves go with it. A save is a snapshot of one game and means nothing
	// without it, so leaving them behind would keep the largest rows in the
	// database and make the deletion no deletion at all.
	Delete(ctx context.Context, userID, id string) error

	// CreateSave writes a snapshot of the user's game under snapshot.Name,
	// replacing whatever the game already holds under that name and reporting
	// whether it replaced one. Names are matched without regard to case.
	//
	// It returns ErrTooManySaves rather than writing a name the game does not
	// already hold once the game holds MaxSavesPerGame of them.
	CreateSave(ctx context.Context, snapshot Snapshot) (Save, bool, error)

	// Saves returns the game's saves, newest first, without their bytes.
	Saves(ctx context.Context, userID, gameID string) ([]Save, error)

	// LoadSave returns one save with the bytes that restore it, or
	// ErrSaveNotFound.
	LoadSave(ctx context.Context, userID, gameID, saveID string) (Snapshot, error)

	// DeleteSave removes one save and returns what it removed, or
	// ErrSaveNotFound.
	DeleteSave(ctx context.Context, userID, gameID, saveID string) (Save, error)
}

// A Turn is what one submitted line produced.
//
// A line is not necessarily a turn of the story: SAVE, RESTORE and RESTART are
// answered by this application, and Intent says which happened. A caller that
// only ever plays the story still has to look, because a player may type any of
// those words at any prompt.
type Turn struct {
	// Intent is who answered the line.
	Intent Intent

	// Session is the stored session as the line left it. It is the zero value
	// when Asked is true, because a question changes nothing.
	Session Session

	// Result is the engine's, and is meaningful only when Intent is
	// IntentPlay.
	Result zmachine.Result

	// Save is the save written or restored, when the line named one.
	Save Save

	// Replaced records that the save written took the place of one the game
	// already held under that name.
	Replaced bool

	// Asked records that the line was a bare SAVE or RESTORE, or a RESTART.
	// Nothing was written and nothing was played: the player has still to be
	// asked for a name, shown the saves to choose between, or asked to confirm.
	Asked bool
}

// A Summary is one of a user's games without the bytes that resume it.
//
// A list of games is read to choose between them, not to play them, and hauling
// every state and every transcript out of the database to draw a menu would be
// paying for what nobody asked to see.
type Summary struct {
	ID        string
	StoryKey  StoryKey
	Turn      int
	Halted    bool
	UpdatedAt time.Time
}

// A Service plays turns against stored sessions.
//
// It is the whole read-run-write cycle with nothing above it: a request handler
// is this with an HTTP request in front. One Service serves every player and is
// safe for concurrent use.
type Service struct {
	library *Library
	runner  *Runner
	store   Store
	locks   keyedMutex
}

// NewService returns a Service. All three arguments are required.
func NewService(library *Library, runner *Runner, store Store) (*Service, error) {
	if library == nil {
		return nil, errors.New("game: service: nil library")
	}
	if runner == nil {
		return nil, errors.New("game: service: nil runner")
	}
	if store == nil {
		return nil, errors.New("game: service: nil store")
	}
	return &Service{library: library, runner: runner, store: store}, nil
}

// NewGame starts a story and stores the state its opening turn produced.
//
// The returned Result holds the banner and opening room; the returned Session
// is what later turns are played against.
func (s *Service) NewGame(ctx context.Context, userID, storyID string) (Session, zmachine.Result, error) {
	if userID == "" {
		return Session{}, zmachine.Result{}, errors.New("game: new game: no user")
	}

	entry, ok := s.library.ByID(storyID)
	if !ok {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: %s: %w", storyID, ErrStoryUnavailable)
	}

	result, err := s.runner.Start(ctx, entry)
	if err != nil {
		return Session{}, zmachine.Result{}, err
	}

	session, err := s.store.Create(ctx, Session{
		UserID:     userID,
		StoryKey:   entry.Key,
		State:      result.State,
		Transcript: result.Output,
		Status:     statusOf(result.StatusLine),
		Halted:     result.Status == zmachine.Halted,
	})
	if err != nil {
		return Session{}, zmachine.Result{}, fmt.Errorf("game: %s: create session: %w", storyID, err)
	}

	return session, result, nil
}

// Play answers one line the player submitted.
//
// SAVE, RESTORE and RESTART are answered here and never reach [Runner.Run]: in
// Version 3 the story's own save reports failure without branching, so a player
// who typed SAVE would see "Failed." — and this application owns persistence
// anyway. RESTART is intercepted because the screen beside the state is ours;
// see [Service.Restart]. The interception is in the service rather than in a
// request handler so that there is no way to reach the engine that goes around
// it.
//
// A bare SAVE or RESTORE, or a RESTART, returns a Turn with Asked set: it is a
// question, and nothing is written until the answer arrives. A save or restore
// with a name on the line is carried out here and now.
//
// An ordinary line is a turn of the story. The session is locked for the whole
// read-run-write cycle, so two commands submitted at once are played one after
// the other rather than both against the same state. The lock covers this
// process; the conditional write covers the rest, and a turn that loses the race
// is refused with ErrVersionConflict rather than replayed against a state the
// player never saw.
//
// A failed turn writes nothing. The Turn returned with the error is the zero
// value; the stored session is still the good one, and the command may be tried
// again if [Classify] says the fault is retryable.
func (s *Service) Play(ctx context.Context, userID, sessionID, command string) (Turn, error) {
	if userID == "" {
		return Turn{}, errors.New("game: play: no user")
	}

	switch line := Interpret(command); line.Intent {
	case IntentSave:
		return s.playSave(ctx, userID, sessionID, line)
	case IntentRestore:
		return s.playRestore(ctx, userID, sessionID, line)
	case IntentRestart:
		// A restart throws away the game in progress, so it is always a
		// question. [Service.Restart] carries it out once the answer arrives.
		return Turn{Intent: IntentRestart, Asked: true}, nil
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	session, err := s.store.Load(ctx, userID, sessionID)
	if err != nil {
		return Turn{}, fmt.Errorf("game: session %s: load: %w", sessionID, err)
	}
	if session.Halted {
		return Turn{}, fmt.Errorf("game: session %s: %w", sessionID, ErrGameOver)
	}

	entry, ok := s.library.ByKey(session.StoryKey)
	if !ok {
		return Turn{}, fmt.Errorf("game: session %s: story %s: %w",
			sessionID, session.StoryKey, ErrStoryUnavailable)
	}

	result, err := s.runner.Run(ctx, entry, session.State, command)
	if err != nil {
		return Turn{}, err
	}

	// A halted story returns no state, and storing that nil is the one time
	// replacing a good state with nothing is right: the session is over and
	// there is nothing left to resume.
	session.State = result.State
	session.Transcript = appendTranscript(session.Transcript, command, result.Output)
	session.Status = statusOf(result.StatusLine)
	session.Halted = result.Status == zmachine.Halted
	session.Turn++

	session, err = s.store.Update(ctx, session)
	if err != nil {
		return Turn{}, fmt.Errorf("game: session %s: store turn %d: %w",
			sessionID, session.Turn, err)
	}

	return Turn{Intent: IntentPlay, Session: session, Result: result}, nil
}

// playSave answers a SAVE the player typed.
func (s *Service) playSave(ctx context.Context, userID, sessionID string, line Command) (Turn, error) {
	if line.Name == "" {
		return Turn{Intent: IntentSave, Asked: true}, nil
	}

	clean, err := CleanSaveName(line.Name)
	if err != nil {
		return Turn{}, err
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	session, save, replaced, err := s.save(ctx, userID, sessionID, clean, line.Text)
	if err != nil {
		return Turn{}, err
	}

	return Turn{Intent: IntentSave, Session: session, Save: save, Replaced: replaced}, nil
}

// playRestore answers a RESTORE the player typed.
//
// A name on the line is matched against the game's saves rather than looked up
// by identifier: the player typed something they read off the list, and the
// identifiers are the database's business.
func (s *Service) playRestore(ctx context.Context, userID, sessionID string, line Command) (Turn, error) {
	if line.Name == "" {
		return Turn{Intent: IntentRestore, Asked: true}, nil
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	saves, err := s.store.Saves(ctx, userID, sessionID)
	if err != nil {
		return Turn{}, fmt.Errorf("game: session %s: list saves: %w", sessionID, err)
	}

	wanted, ok := findSave(saves, strings.Join(strings.Fields(line.Name), " "))
	if !ok {
		return Turn{}, fmt.Errorf("game: session %s: save %q: %w", sessionID, line.Name, ErrSaveNotFound)
	}

	session, save, err := s.restore(ctx, userID, sessionID, wanted.ID, line.Text)
	if err != nil {
		return Turn{}, err
	}

	return Turn{Intent: IntentRestore, Session: session, Save: save}, nil
}

// Restart begins the session's story again, once the player has confirmed.
//
// The engine needs no help to reset the machine — RESTART is an opcode, and a
// fresh [Runner.Start] produces exactly the state it would have. What the engine
// cannot do is put back the screen this application keeps beside that state:
// nothing in a Result says a restart happened, so a RESTART played as an
// ordinary turn would leave the abandoned game in the transcript with the new
// banner underneath it and the move count still climbing. The opening is
// therefore stored the way [Service.NewGame] stores it, replacing the transcript
// rather than adding to it and putting the turn count back to zero.
//
// The game's named saves are deliberately kept. Each is a complete state and the
// story has not changed, so every one of them still restores; throwing them away
// would be the destructive choice, and it is not the one a player asking to
// start over is asking for.
//
// The session is locked for the whole cycle and the write is conditional, as in
// [Play]: a restart is a write like any other and a turn issued against the
// screen it replaced is refused rather than replayed.
func (s *Service) Restart(ctx context.Context, userID, sessionID string) (Turn, error) {
	if userID == "" {
		return Turn{}, errors.New("game: restart: no user")
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	session, err := s.store.Load(ctx, userID, sessionID)
	if err != nil {
		return Turn{}, fmt.Errorf("game: session %s: load: %w", sessionID, err)
	}

	// A halted session is restartable, unlike a played one: a story that ended
	// itself is exactly the one a player wants to begin again, and beginning
	// again does not need the state that ended.

	entry, ok := s.library.ByKey(session.StoryKey)
	if !ok {
		return Turn{}, fmt.Errorf("game: session %s: story %s: %w",
			sessionID, session.StoryKey, ErrStoryUnavailable)
	}

	result, err := s.runner.Start(ctx, entry)
	if err != nil {
		return Turn{}, err
	}

	session.State = result.State
	session.Transcript = result.Output
	session.Status = statusOf(result.StatusLine)
	session.Halted = result.Status == zmachine.Halted
	session.Turn = 0

	session, err = s.store.Update(ctx, session)
	if err != nil {
		return Turn{}, fmt.Errorf("game: session %s: store restart: %w", sessionID, err)
	}

	return Turn{Intent: IntentRestart, Session: session, Result: result}, nil
}

// Games returns the user's games, most recently played first.
func (s *Service) Games(ctx context.Context, userID string) ([]Summary, error) {
	if userID == "" {
		return nil, errors.New("game: games: no user")
	}

	summaries, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("game: user %s: list games: %w", userID, err)
	}
	return summaries, nil
}

// Delete throws a game away, once the player has confirmed.
//
// Every save the game holds goes with it, and unlike a save there is nothing
// left to restore from: a caller that has not asked first is destroying
// something the player cannot get back.
//
// The session is locked for the same reason a turn is. A delete that ran beside
// a turn would leave the turn writing to a row that is no longer there, and the
// player would read the failure as the command going wrong rather than as the
// game having been thrown away.
func (s *Service) Delete(ctx context.Context, userID, sessionID string) error {
	if userID == "" {
		return errors.New("game: delete: no user")
	}

	unlock := s.locks.lock(sessionID)
	defer unlock()

	if err := s.store.Delete(ctx, userID, sessionID); err != nil {
		return fmt.Errorf("game: session %s: delete: %w", sessionID, err)
	}
	return nil
}

// Session returns a stored session without playing anything, for a client that
// is reconnecting rather than issuing a command.
func (s *Service) Session(ctx context.Context, userID, sessionID string) (Session, error) {
	if userID == "" {
		return Session{}, errors.New("game: session: no user")
	}

	session, err := s.store.Load(ctx, userID, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("game: session %s: load: %w", sessionID, err)
	}
	return session, nil
}

// keyedMutex serializes work per session identifier.
//
// Entries are dropped when the last holder leaves, so a server that has seen a
// million sessions is not still holding a million mutexes.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu      sync.Mutex
	holders int
}

// lock blocks until the key is free and returns the function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	entry, ok := k.locks[key]
	if !ok {
		entry = &keyedLock{}
		k.locks[key] = entry
	}
	entry.holders++
	k.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		k.mu.Lock()
		entry.holders--
		if entry.holders == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
