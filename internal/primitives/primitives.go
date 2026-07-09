// Package primitives implements per-user behavioral memory for tool/capability
// "primitives" — Phase 6 DoD item 2 ("friction detection triggers a rewrite of
// a primitive per user_id") and docs/architecture/memory-tiers.md section
// "Primitive self-improvement (behavioral memory)".
//
// A Primitive is a per-(tenant, user) clone of a tool's *mutable guidance
// metadata*: the description the LLM reads and its few-shot Examples.
// Executable code is never cloned, only the metadata an LLM interprets (see
// docs/vision/thesis.md and docs/architecture/granular-control.md) — this
// package therefore has no dependency on internal/tools and never touches
// tool implementations, only their per-user description/example overlay.
//
// When a user repeatedly hits friction with a primitive — failed or retried
// invocations, or an explicit correction — its FrictionScore accrues via
// FrictionDetector.Observe or Store.RecordFriction. The offline sleep cycle
// (internal/sleepcycle, Phase 6) periodically calls Consolidate, which scans
// a Store for primitives whose FrictionScore has crossed a threshold and
// rewrites each one through a Rewriter — by default the deterministic,
// LLM-free HeuristicRewriter, which sharpens the description and appends a
// corrective example — then resets FrictionScore to 0.
//
// Idempotency: a rewrite bumps Revision and resets FrictionScore, so calling
// Consolidate again with no new friction in between rewrites zero primitives.
// This mirrors the idempotency discipline used elsewhere in aisotropy (see
// internal/facts, whose Save/Supersede are similarly safe to retry).
//
// Isolation: primitives are keyed by (tenant, user, name); friction recorded
// for one (tenant, user) pair never affects another's primitive of the same
// name, matching the tenant/user isolation gate in
// .claude/rules/constitutional-gates.md.
package primitives

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Primitive is a per-user clone of a tool's mutable guidance metadata: the
// description and few-shot examples an LLM is shown when the tool is
// injected into a request, plus the bookkeeping needed to detect and react to
// behavioral friction.
type Primitive struct {
	// Name identifies the underlying tool/capability this primitive clones
	// (e.g. matches a internal/tools.Tool.Name).
	Name string
	// UserID and TenantID identify the owner of this clone. Two different
	// users may each have their own Primitive for the same Name.
	UserID   string
	TenantID string
	// Description is the guidance text shown to the LLM for this primitive.
	// It mutates over time as friction is corrected.
	Description string
	// Examples are few-shot examples shown alongside Description. Rewrites
	// append to this slice rather than replacing it, so corrective guidance
	// accumulates.
	Examples []string
	// FrictionScore accrues as failed/retried invocations or corrections are
	// observed. Consolidate rewrites primitives at or above a threshold and
	// resets this to 0.
	FrictionScore float64
	// Revision counts how many times this primitive has been rewritten. It
	// starts at 0 for a primitive that has never been rewritten.
	Revision int
	// UpdatedAt is the last time this primitive was written (created,
	// friction recorded, or rewritten).
	UpdatedAt time.Time
}

// key identifies a Primitive within a Store.
type key struct {
	tenant string
	user   string
	name   string
}

func newKey(tenant, user, name string) key {
	return key{tenant: tenant, user: user, name: name}
}

// Store is an in-memory, thread-safe registry of Primitives keyed by
// (tenant, user, name). All operations are safe for concurrent use.
//
// Store deliberately holds no persistence backend of its own: it is the
// working-set view the sleep cycle (internal/sleepcycle) and request path
// share in-process. A durable-storage-backed Store can be added later behind
// the same method set without changing callers.
type Store struct {
	mu    sync.Mutex
	items map[key]Primitive
}

// NewStore returns an empty Store ready to use.
func NewStore() *Store {
	return &Store{items: make(map[key]Primitive)}
}

// cloneExamples returns p with an independent copy of its Examples slice, so a
// stored Primitive never shares its backing array with a caller's slice in
// either direction. Primitive is held by value in the map, but its Examples
// slice header is copied by assignment — leaving the backing array shared. A
// shared array would let a mutation (or an in-cap append) on one side silently
// corrupt the other; cloning on every read and write closes that aliasing
// hazard. A nil slice stays nil.
func cloneExamples(p Primitive) Primitive {
	if p.Examples != nil {
		p.Examples = append([]string(nil), p.Examples...)
	}
	return p
}

// itemsLocked lazily initializes the map so a zero-value Store is safe to write
// to. Callers MUST hold s.mu.
func (s *Store) itemsLocked() map[key]Primitive {
	if s.items == nil {
		s.items = make(map[key]Primitive)
	}
	return s.items
}

// Get returns the primitive registered at (tenant, user, name), or
// (Primitive{}, false) if none exists. The returned Primitive owns its
// Examples slice; mutating it does not affect the stored primitive.
func (s *Store) Get(tenant, user, name string) (Primitive, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[newKey(tenant, user, name)]
	if !ok {
		return Primitive{}, false
	}
	return cloneExamples(p), true
}

// Put upserts p, keyed by (p.TenantID, p.UserID, p.Name), overwriting any
// existing primitive at that key. If p.UpdatedAt is the zero time, Put fills
// it in with the current time. The stored primitive keeps an independent copy
// of p.Examples, so the caller may reuse or mutate its slice afterward.
func (s *Store) Put(p Primitive) {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.itemsLocked()[newKey(p.TenantID, p.UserID, p.Name)] = cloneExamples(p)
}

// ListForUser returns every primitive registered for (tenant, user). Order is
// unspecified. Each returned Primitive owns its Examples slice.
func (s *Store) ListForUser(tenant, user string) []Primitive {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Primitive
	for k, p := range s.items {
		if k.tenant == tenant && k.user == user {
			out = append(out, cloneExamples(p))
		}
	}
	return out
}

// Snapshot returns every primitive currently in the store, across all
// tenants and users. Order is unspecified. Consolidate uses Snapshot to scan
// for friction without holding the Store lock while it calls out to a
// Rewriter, which may be slow (e.g. LLM-backed in a future implementation).
func (s *Store) Snapshot() []Primitive {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Primitive, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, cloneExamples(p))
	}
	return out
}

// RecordFriction increments the FrictionScore of (tenant, user, name) by
// delta and returns the resulting Primitive. If no primitive is registered
// yet at that key, RecordFriction creates one first (populating only
// Name/UserID/TenantID) so friction can be tracked even before the caller has
// explicit guidance metadata to store — a first failed invocation may arrive
// before any Put. delta must be >= 0; friction only accrues here, use
// ResetFriction (or a rewrite via Consolidate) to clear it.
func (s *Store) RecordFriction(tenant, user, name string, delta float64) (Primitive, error) {
	if delta < 0 {
		return Primitive{}, fmt.Errorf("primitives: RecordFriction delta must be >= 0, got %v", delta)
	}
	k := newKey(tenant, user, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[k]
	if !ok {
		p = Primitive{Name: name, UserID: user, TenantID: tenant}
	}
	p.FrictionScore += delta
	p.UpdatedAt = time.Now().UTC()
	s.itemsLocked()[k] = p
	// Return a copy that does not share the stored Examples backing array.
	return cloneExamples(p), nil
}

// ResetFriction zeroes the FrictionScore of (tenant, user, name), leaving all
// other fields untouched. It is a no-op if no primitive is registered at that
// key. Rewrite paths call this (via Put) after producing a revised
// Primitive; ResetFriction is exposed directly for callers that want to
// clear friction without a full rewrite (e.g. a manual operator override).
func (s *Store) ResetFriction(tenant, user, name string) {
	k := newKey(tenant, user, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[k]
	if !ok {
		return
	}
	p.FrictionScore = 0
	p.UpdatedAt = time.Now().UTC()
	s.items[k] = p
}

// FrictionSignal is an observed interaction outcome that contributes to a
// primitive's FrictionScore. Different signals carry different weight: an
// explicit user correction is stronger evidence of friction than a single
// reflection-loop retry.
type FrictionSignal int

const (
	// SignalRetry is a reflection-loop retry of a primitive invocation (see
	// .claude/rules/constitutional-gates.md "Structured-output / reflection
	// loop"): the first attempt did not satisfy the expected schema/outcome.
	SignalRetry FrictionSignal = iota
	// SignalFailure is a primitive invocation that failed outright (no
	// successful retry).
	SignalFailure
	// SignalUserCorrection is the user explicitly correcting the bot's use of
	// the primitive — the strongest, most explicit friction signal.
	SignalUserCorrection
)

// weight returns the FrictionScore delta a signal contributes.
func (s FrictionSignal) weight() float64 {
	switch s {
	case SignalRetry:
		return 1.0
	case SignalFailure:
		return 2.0
	case SignalUserCorrection:
		return 3.0
	default:
		return 1.0
	}
}

// FrictionDetector records friction signals against a Store's primitives. It
// is the entry point invocation-handling code calls when a tool invocation
// for a cloned primitive fails, is retried by the reflection loop, or is
// explicitly corrected by the user.
type FrictionDetector struct {
	store *Store
}

// NewFrictionDetector returns a FrictionDetector that records friction into
// store.
func NewFrictionDetector(store *Store) *FrictionDetector {
	return &FrictionDetector{store: store}
}

// Observe records signal against (tenant, user, name)'s FrictionScore and
// returns the resulting Primitive.
func (d *FrictionDetector) Observe(tenant, user, name string, signal FrictionSignal) (Primitive, error) {
	return d.store.RecordFriction(tenant, user, name, signal.weight())
}

// Rewriter revises a Primitive that has accrued friction, producing the
// version that should replace it in the Store. Implementations should be
// pure with respect to their input (aside from timestamps): given the same
// Primitive, produce an equivalent revision.
type Rewriter interface {
	Rewrite(ctx context.Context, p Primitive) (Primitive, error)
}

// HeuristicRewriter is a deterministic, LLM-free Rewriter: it bumps Revision,
// sharpens Description with a revision marker, appends a corrective example
// naming the primitive, and resets FrictionScore to 0. It exists so Phase 6
// primitive self-improvement is fully testable without a live model. A
// future LLM-backed Rewriter (an "optimizer agent", per
// docs/architecture/memory-tiers.md) can implement the same Rewriter
// interface as a drop-in replacement.
type HeuristicRewriter struct{}

// Rewrite implements Rewriter. It always produces a revised Primitive
// regardless of the input's FrictionScore; gating rewrites by a friction
// threshold is Consolidate's responsibility, not the Rewriter's.
func (HeuristicRewriter) Rewrite(_ context.Context, p Primitive) (Primitive, error) {
	p.Revision++
	p.Description = sharpenDescription(p.Description, p.Revision)
	p.Examples = append(append([]string(nil), p.Examples...), correctiveExample(p.Name, p.Revision))
	p.FrictionScore = 0
	p.UpdatedAt = time.Now().UTC()
	return p, nil
}

func sharpenDescription(desc string, revision int) string {
	marker := fmt.Sprintf("[revised r%d: be precise about required inputs and the expected output shape]", revision)
	if desc == "" {
		return marker
	}
	return desc + " " + marker
}

func correctiveExample(name string, revision int) string {
	return fmt.Sprintf(
		"Note (r%d): previous attempts with %q ran into friction; be more explicit about required arguments and the expected output format before invoking it.",
		revision, name,
	)
}

// Consolidate scans store for primitives whose FrictionScore is >= threshold
// and rewrites each one in place via rewriter. This is the function the
// Phase 6 sleep cycle (internal/sleepcycle) calls during cold consolidation
// (docs/architecture/memory-tiers.md "Primitive self-improvement"). It
// returns the number of primitives rewritten.
//
// Consolidate is idempotent: since a rewrite resets FrictionScore to 0 (see
// HeuristicRewriter.Rewrite), a primitive is not rewritten again until it
// accrues new friction >= threshold. Calling Consolidate twice in a row with
// no new friction recorded in between rewrites 0 primitives on the second
// call.
//
// Consolidate reads a Snapshot up front and writes rewritten primitives back
// with Put; it does not hold the Store lock while calling rewriter.Rewrite.
// A primitive that receives new friction concurrently with its own
// consolidation pass may have that concurrent friction overwritten by the
// rewrite's Put — Consolidate is designed for the sleep cycle's offline,
// non-concurrent-with-request-path batch window, not for interleaving with
// live traffic on the same primitive.
func Consolidate(ctx context.Context, store *Store, rewriter Rewriter, threshold float64) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("primitives: Consolidate requires a non-nil store")
	}
	if rewriter == nil {
		return 0, fmt.Errorf("primitives: Consolidate requires a non-nil rewriter")
	}
	rewritten := 0
	for _, p := range store.Snapshot() {
		if p.FrictionScore < threshold {
			continue
		}
		select {
		case <-ctx.Done():
			return rewritten, ctx.Err()
		default:
		}
		revised, err := rewriter.Rewrite(ctx, p)
		if err != nil {
			return rewritten, fmt.Errorf("primitives: rewrite %s/%s/%s: %w", p.TenantID, p.UserID, p.Name, err)
		}
		store.Put(revised)
		rewritten++
	}
	return rewritten, nil
}
