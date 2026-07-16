package taskagent

// Session resume for the sandboxed task runner (ADR-0037).
//
// # What a resumed task actually restores (the honest semantics)
//
// A resumed task does NOT restore a filesystem snapshot, and it deliberately
// cannot: RunTask stands up an EPHEMERAL sandbox container over a FRESH git
// worktree on every run, and the container is torn down at the end of the run —
// there is nothing to "reattach" to. What a resume restores is the AGENT'S
// CONTEXT — the ordered step trace (the tool decisions and their results) and
// the task prompt that produced them — i.e. the agent's "mind", not the box its
// hands ran in. The prior run's commits were already merged into the
// repository's HEAD (BranchStrategy MergeToHead), so a resumed run simply works
// off the UPDATED repo and is handed a rendered history of what the earlier run
// did, injected into its prompt (see resumePrompt).
//
// This is the real, honest advantage over a CLI tool that resumes from a local
// JSONL transcript: the session state here is TENANT-SCOPED (keyed by the
// tenant/user that travel via ctx, exactly like the working-memory chat tier)
// and PERSISTENT in a store the runtime owns — not a per-machine file a single
// operator shuffles around. The key-broker invariant is untouched: session
// state is host-side metadata; no provider key ever entered any container, and
// resume adds no new path for one to.
//
// # Why a dedicated store, not internal/memory.WorkingMemory
//
// WorkingMemory was evaluated first (it is aisotropy's own persistence for the
// chat tier) and rejected for THIS job because its semantics are lossy for a
// step trace:
//
//   - WorkingMemory bounds its active buffer to ~12 messages and asynchronously
//     CONDENSES (LLM-summarizes) and PRUNES the oldest ones into a single
//     summary string. A resumed task must replay its prior steps VERBATIM; a
//     task with more steps than the window — or a session resumed several times
//     — would silently lose its earliest steps to condensation. "Memory of the
//     prior run" would degrade into a lossy digest.
//   - An AgentStep is structured (tool + typed args + result), not a
//     role/content chat turn; forcing it through memory.Message would bury the
//     structure inside an opaque JSON-in-Content blob.
//   - A resume wants the WHOLE prior trace atomically, whereas WorkingMemory
//     exposes only "the last N" (Recent) plus a condensed Summary.
//
// This store instead keeps the full SessionState as one exact, non-lossy record
// per (tenant, user, session). It reuses the SAME isolation primitive the
// working-memory tier uses — memory.ValidateIdentityComponent, which is
// exported precisely so identity carriers outside that package share one
// character-class gate — and the BadgerDB implementation reuses the same
// embedded engine, so "leverage aisotropy's own persistence" holds at the
// substrate level without inheriting the chat buffer's lossy windowing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dgraph-io/badger/v4"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// defaultSessionScope is the tenant/user component used when no identity is
// present on ctx — the standalone, single-tenant case (a bare RunTask with no
// llm.WithTenant/WithUser injected). A real multi-tenant deployment MUST inject
// identity via ctx (exactly as the HTTP chat path does) so sessions are keyed
// by the true tenant/user and one tenant can never resume another's session;
// with no identity every task shares this single local scope. It is a fixed,
// reserved-character-free sentinel so resolveSessionKey.validate always passes.
const defaultSessionScope = "_local"

// SessionKey identifies one resumable task session. Tenant and User are NOT
// carried on the Request (the multi-tenancy gate: tenant identity travels via
// ctx, never as a data field) — they are read from ctx at RunTask time by
// resolveSessionKey; Session is the caller-supplied Request.SessionID. All three
// bound every stored record's key, so a store read scoped to one SessionKey can
// never observe another's state.
type SessionKey struct {
	Tenant  string
	User    string
	Session string
}

// validate applies the same non-empty / no-reserved-character rule the
// working-memory tier applies to a memory.Key, reusing memory's exported gate so
// the two identity surfaces cannot drift. A component that could smuggle the
// key separator could forge a cross-tenant prefix collision — rejecting it here
// is the isolation enforcement point at the key level.
func (k SessionKey) validate() error {
	if err := memory.ValidateIdentityComponent("tenant", k.Tenant); err != nil {
		return err
	}
	if err := memory.ValidateIdentityComponent("user", k.User); err != nil {
		return err
	}
	return memory.ValidateIdentityComponent("session", k.Session)
}

// storageKey is the flat, scoped key a persistent store files this session's
// record under. The tenant/user/session segments are validated (no reserved
// character can appear in them), so the segments are unambiguous and one
// tenant's namespace can never be entered by another.
func (k SessionKey) storageKey() []byte {
	return []byte(fmt.Sprintf("tasksession:tenant:%s:user:%s:session:%s", k.Tenant, k.User, k.Session))
}

// SessionState is the persisted "mind" of a task session: the ordered step
// trace and the most recent task prompt that produced it. It is exactly what a
// Resume run rehydrates — the agent's context/history, NOT the filesystem (the
// files already reflect the prior run's merged commits; see this file's package
// doc). Steps accumulate across resumes; Prompt holds the latest run's prompt.
type SessionState struct {
	// Prompt is the task instruction of the most recent run in this session.
	Prompt string `json:"prompt"`
	// Steps is the full, verbatim step trace accumulated across every run in
	// this session, oldest first — never condensed or truncated.
	Steps []AgentStep `json:"steps"`
}

// clone returns a deep copy so a store never hands back a slice/map a caller can
// mutate into the stored record (or vice-versa) — the same defensive-copy
// discipline the in-memory store relies on for isolation.
func (s SessionState) clone() SessionState {
	out := SessionState{Prompt: s.Prompt}
	if len(s.Steps) == 0 {
		return out
	}
	out.Steps = make([]AgentStep, len(s.Steps))
	for i, st := range s.Steps {
		cp := AgentStep{Tool: st.Tool, Result: st.Result}
		if st.Args != nil {
			cp.Args = make(map[string]string, len(st.Args))
			for k, v := range st.Args {
				cp.Args[k] = v
			}
		}
		out.Steps[i] = cp
	}
	return out
}

// SessionStore persists and loads a task's SessionState per (tenant, user,
// session). It is the seam RunTask writes the step trace to after a run and
// reads it back from on a Resume. Implementations MUST scope every read/write to
// the full SessionKey so one session can never observe another's state (the
// multi-tenant isolation gate). A nil store on a Request disables persistence
// and resume entirely (the default).
type SessionStore interface {
	// Load returns the stored state for key. found is false (with a zero state
	// and nil error) when no run has ever persisted this session — a first-time
	// resume of an unknown session is NOT an error, it simply starts cold. A
	// non-nil error signals an actual store failure.
	Load(ctx context.Context, key SessionKey) (state SessionState, found bool, err error)
	// Save writes state as the whole record for key, replacing any prior record.
	// RunTask hands it the accumulated (prior + this run's) steps, so "replace"
	// and "append across resumes" coincide.
	Save(ctx context.Context, key SessionKey, state SessionState) error
}

// resolveSessionKey builds the SessionKey for this run: tenant/user read from
// ctx (the identity the caller injected via llm.WithTenant/WithUser, mirroring
// the chat path), falling back to defaultSessionScope for the standalone
// single-tenant case, and session from the caller-supplied id. The resulting key
// is validated before it can reach any store.
func resolveSessionKey(ctx context.Context, sessionID string) (SessionKey, error) {
	tenant, ok := llm.TenantFromContext(ctx)
	if !ok || tenant == "" {
		tenant = defaultSessionScope
	}
	user, ok := llm.UserFromContext(ctx)
	if !ok || user == "" {
		user = defaultSessionScope
	}
	key := SessionKey{Tenant: tenant, User: user, Session: sessionID}
	if err := key.validate(); err != nil {
		return SessionKey{}, err
	}
	return key, nil
}

// resumePrompt renders the prior session state into a preamble prepended to the
// current task prompt, so the agent loop "sees" what the earlier run did without
// the sandbox it ran in. It states the honest resume contract to the model:
// the prior commits are already in the repo it now sees, and this is a fresh
// workspace. Results are trimmed to keep the injected context bounded.
func resumePrompt(prior SessionState, current string) string {
	var b strings.Builder
	b.WriteString("This task session is being RESUMED. A prior run already executed against this ")
	b.WriteString("repository and its commits are ALREADY merged into the repository you now see, ")
	b.WriteString("so the files reflect that earlier work. You are in a FRESH workspace — the prior ")
	b.WriteString("sandbox no longer exists; only the history below is carried over.\n\n")
	if strings.TrimSpace(prior.Prompt) != "" {
		b.WriteString("Prior task: ")
		b.WriteString(prior.Prompt)
		b.WriteString("\n")
	}
	if len(prior.Steps) > 0 {
		b.WriteString("Prior steps:\n")
		for i, s := range prior.Steps {
			tool := s.Tool
			if tool == "" {
				tool = "(final answer, no tool)"
			}
			fmt.Fprintf(&b, "  [%d] %s -> %s\n", i+1, tool, previewResult(s.Result))
		}
	}
	b.WriteString("\nCurrent task:\n")
	b.WriteString(current)
	return b.String()
}

// previewResult trims a step result to a bounded single-line snippet for the
// resume preamble so a long tool output does not blow up the injected context.
func previewResult(s string) string {
	const max = 200
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// InMemorySessions is a process-lifetime, tenant-scoped SessionStore backed by a
// mutex-guarded map. It persists across RunTask calls for as long as the store
// value is kept alive (e.g. a long-running server that reuses one store across
// many task requests), but NOT across process restarts — use OpenBadgerSessions
// for that. Every record is stored and returned by deep copy, so a caller can
// neither mutate another session's state nor a stored record out from under the
// store.
type InMemorySessions struct {
	mu sync.Mutex
	m  map[string]SessionState
}

// NewInMemorySessions returns an empty in-memory session store.
func NewInMemorySessions() *InMemorySessions {
	return &InMemorySessions{m: make(map[string]SessionState)}
}

// Load returns the stored state for key (found=false when absent).
func (s *InMemorySessions) Load(_ context.Context, key SessionKey) (SessionState, bool, error) {
	if err := key.validate(); err != nil {
		return SessionState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[string(key.storageKey())]
	if !ok {
		return SessionState{}, false, nil
	}
	return st.clone(), true, nil
}

// Save replaces the record for key with a deep copy of state.
func (s *InMemorySessions) Save(_ context.Context, key SessionKey, state SessionState) error {
	if err := key.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(key.storageKey())] = state.clone()
	return nil
}

// BadgerSessions is a persistent, tenant-scoped SessionStore backed by the same
// embedded BadgerDB engine the working-memory tier uses — the "leverage
// aisotropy's own persistence" substrate — storing each SessionState as one
// exact JSON record under its scoped, validated key. Unlike WorkingMemory it
// applies NO windowing or condensation, so a step trace round-trips verbatim
// across resumes and process restarts. Close it when done.
type BadgerSessions struct {
	db *badger.DB
}

// OpenBadgerSessions opens (creating if needed) an on-disk BadgerDB session
// store rooted at dir. The returned store survives process restarts; the caller
// owns its lifecycle and must Close it.
func OpenBadgerSessions(dir string) (*BadgerSessions, error) {
	db, err := badger.Open(tuneSessionBadger(badger.DefaultOptions(dir)))
	if err != nil {
		return nil, fmt.Errorf("taskagent: open session store at %q: %w", dir, err)
	}
	return &BadgerSessions{db: db}, nil
}

// newInMemoryBadgerSessions opens a BadgerDB session store with no on-disk
// footprint — the exact BadgerSessions code path (JSON encode, scoped key,
// txn Get/Set) exercised without a temp dir, for tests and benchmarks.
func newInMemoryBadgerSessions() (*BadgerSessions, error) {
	db, err := badger.Open(tuneSessionBadger(badger.DefaultOptions("").WithInMemory(true)))
	if err != nil {
		return nil, fmt.Errorf("taskagent: open in-memory session store: %w", err)
	}
	return &BadgerSessions{db: db}, nil
}

// tuneSessionBadger silences BadgerDB's stdout logger and keeps a single version
// per key (this store overwrites a session's record in place; it never needs
// historical versions). It intentionally leaves the size defaults alone: unlike
// the chat buffer this store holds at most one small record per session and is
// not a hot path, so the working-memory tier's aggressive footprint tuning
// buys nothing here.
func tuneSessionBadger(opts badger.Options) badger.Options {
	return opts.WithLogger(nil).WithNumVersionsToKeep(1)
}

// Load reads and JSON-decodes the stored record for key (found=false when the
// key is absent, which a first-time resume treats as "start cold").
func (s *BadgerSessions) Load(_ context.Context, key SessionKey) (SessionState, bool, error) {
	if err := key.validate(); err != nil {
		return SessionState{}, false, err
	}
	var (
		state SessionState
		found bool
	)
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key.storageKey())
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &state)
		})
	})
	if err != nil {
		return SessionState{}, false, fmt.Errorf("taskagent: load session: %w", err)
	}
	return state, found, nil
}

// Save JSON-encodes state and writes it as the whole record for key.
func (s *BadgerSessions) Save(_ context.Context, key SessionKey, state SessionState) error {
	if err := key.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("taskagent: encode session state: %w", err)
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key.storageKey(), payload)
	}); err != nil {
		return fmt.Errorf("taskagent: save session: %w", err)
	}
	return nil
}

// Close releases the underlying BadgerDB instance.
func (s *BadgerSessions) Close() error { return s.db.Close() }
