package orbit

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// This file exercises Orbit feature #1 (tenant isolation) against Eino's REAL
// state model: compose.CheckPointStore + compose.WithCheckPointID.
//
// Category note: CheckPointStore is a human-in-the-loop INTERRUPT/RESUME KV, not
// a memory tier. Eino persists graph state at an interrupt by serializing it into
// the store keyed by ONE developer-supplied string (the checkpoint ID). The
// interface (from the real framework) is literally:
//
//	type CheckPointStore interface {
//	    Get(ctx, checkPointID string) ([]byte, bool, error)
//	    Set(ctx, checkPointID string, checkPoint []byte) error
//	}
//
// There is no tenant dimension anywhere — Eino is an orchestration library with
// no tenancy primitive by design. If two tenants compose the same checkpoint ID,
// the second tenant's resume loads whatever was stored under that string. That is
// what any string-keyed KV does (including aisotropy's BadgerDB); the point is
// that Eino has no tenant dimension to catch a wrong/short composed key. The
// round-trip below is driven by Eino's real runtime, not by our test harness.
// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md

func init() {
	// Eino serializes checkpoint state via a registered type. Register Orbit's
	// state exactly like the framework's own checkpoint tests register theirs.
	schema.RegisterName[*isoState]("orbit_iso_state")
}

// isoState is the per-run graph state Eino serializes into the checkpoint.
type isoState struct {
	Secret string
}

// InMemoryCheckPointStore is a real implementation of Eino's CheckPointStore.
// It is a bare map[string][]byte — because that is all the interface asks for.
// Nothing here (and nothing Eino provides) scopes a checkpoint to a tenant.
type InMemoryCheckPointStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

var _ compose.CheckPointStore = (*InMemoryCheckPointStore)(nil)

func NewInMemoryCheckPointStore() *InMemoryCheckPointStore {
	return &InMemoryCheckPointStore{m: make(map[string][]byte)}
}

func (s *InMemoryCheckPointStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	return v, ok, nil
}

func (s *InMemoryCheckPointStore) Set(_ context.Context, id string, cp []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = cp
	return nil
}

// Keys returns the raw checkpoint IDs currently stored (evidence of the bare
// string keyspace).
func (s *InMemoryCheckPointStore) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	return out
}

// IsolationHarness owns a real compiled Eino graph and a real checkpoint store.
type IsolationHarness struct {
	runnable compose.Runnable[string, string]
	store    *InMemoryCheckPointStore
	// interruptIDs models a server's session bookkeeping: it maps a checkpoint
	// ID to the interrupt handle Eino returned. CRUCIALLY it has no tenant
	// dimension either — a shared session ID gives a shared interrupt handle.
	// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	interruptIDs map[string]string
}

// NewIsolationHarness builds the real Eino graph:
//
//	START -> remember -> (interrupt) -> recall -> END
//
// `remember` writes the incoming secret into graph state; Eino interrupts and
// checkpoints; `recall` (on resume) reads the secret back out of the restored
// state. Persistence is 100% Eino's real checkpoint machinery.
func NewIsolationHarness(ctx context.Context) (*IsolationHarness, error) {
	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *isoState {
			return &isoState{}
		}),
	)

	if err := g.AddLambdaNode("remember", compose.InvokableLambda(
		func(ctx context.Context, secret string) (string, error) {
			// Write the secret into the graph state Eino will checkpoint.
			if err := compose.ProcessState(ctx, func(_ context.Context, st *isoState) error {
				st.Secret = secret
				return nil
			}); err != nil {
				return "", err
			}
			return secret, nil
		})); err != nil {
		return nil, err
	}

	if err := g.AddLambdaNode("recall", compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			// Read the secret back out of the state Eino restored from the
			// checkpoint. Whatever tenant's state was under the checkpoint ID
			// is what we return.
			var out string
			if err := compose.ProcessState(ctx, func(_ context.Context, st *isoState) error {
				out = st.Secret
				return nil
			}); err != nil {
				return "", err
			}
			return out, nil
		})); err != nil {
		return nil, err
	}

	if err := g.AddEdge(compose.START, "remember"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("remember", "recall"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("recall", compose.END); err != nil {
		return nil, err
	}

	store := NewInMemoryCheckPointStore()
	runnable, err := g.Compile(ctx,
		compose.WithNodeTriggerMode(compose.AllPredecessor),
		compose.WithCheckPointStore(store),
		compose.WithInterruptBeforeNodes([]string{"recall"}),
		compose.WithGraphName("orbit-session"),
	)
	if err != nil {
		return nil, err
	}

	return &IsolationHarness{
		runnable:     runnable,
		store:        store,
		interruptIDs: make(map[string]string),
	}, nil
}

// Seed runs the graph up to the interrupt under checkpoint ID `checkpointID`,
// persisting `secret` into Eino's checkpoint store. This is what a tenant's
// first request does before "awaiting" more input.
func (h *IsolationHarness) Seed(ctx context.Context, checkpointID, secret string) error {
	_, err := h.runnable.Invoke(ctx, secret, compose.WithCheckPointID(checkpointID))
	if err == nil {
		return fmt.Errorf("expected interrupt before recall, got clean completion")
	}
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		return fmt.Errorf("expected an Eino interrupt, got: %w", err)
	}
	if len(info.InterruptContexts) == 0 {
		return fmt.Errorf("interrupt carried no context")
	}
	// Server-side bookkeeping keyed by the (non-tenant-scoped) checkpoint ID.
	h.interruptIDs[checkpointID] = info.InterruptContexts[0].ID
	return nil
}

// Read resumes the graph under checkpoint ID `checkpointID` and returns whatever
// secret Eino restores from that checkpoint. If no checkpoint (and no interrupt
// handle) exists under the ID, it returns ("", false, nil) — a clean miss.
//
// The footgun: call Read with a checkpoint ID that another tenant composed to the
// same string, and Eino hands you what was stored there — a string-keyed KV
// faithfully returning the value under the queried key. Correct, distinct keys
// (Scenario A in the demos) stay isolated.
func (h *IsolationHarness) Read(ctx context.Context, checkpointID string) (string, bool, error) {
	interruptID, ok := h.interruptIDs[checkpointID]
	if !ok {
		// No checkpoint under this exact string key -> nothing to resume.
		return "", false, nil
	}
	rctx := compose.Resume(ctx, interruptID)
	out, err := h.runnable.Invoke(rctx, "", compose.WithCheckPointID(checkpointID))
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// Store exposes the real checkpoint store for direct-probe evidence.
func (h *IsolationHarness) Store() *InMemoryCheckPointStore { return h.store }
