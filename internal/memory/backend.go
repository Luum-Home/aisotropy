// Working-tier backend seam (ADR-0081). This is the interface the ADR deferred
// its remaining decisions behind — "deferred until the interface exists" — and
// its shape is COPIED, not invented: it mirrors facts.DocBackend and
// semantic.VectorBackend (ADR-0034, Tier-2 open core).
//
// The copied property is the one that matters. The driver is deliberately
// tenant-UNAWARE and receives an opaque `ns string` it did not compose and
// cannot decode, while isolation is supplied by a sealed decorator whose
// namespace derivation is unexported. A community backend can never see or
// choose the tenant, so a buggy backend cannot widen a read past its namespace.
//
// ADR-0081's first draft argued the opposite — that the interface should speak
// memory.Key so isolation stayed at L3. That was backwards, and the ADR records
// the correction rather than editing it away: handing every backend the tenant
// identity is strictly worse. L3 survives BECAUSE the driver gets a string it
// cannot interpret.
//
// WHY THE EXISTING KEYS COULD NOT SIMPLY BE FORWARDED. Today's keys embed the
// identity in plaintext — `tenant:{t}:user:{u}:session:{s}:chat:{seq}` (key.go).
// Handing those to a driver would leak exactly what the seal exists to hide, so
// this seam re-derives them: the namespace is a hash of (tenant, user), and the
// session travels as a separate parameter INSIDE that already-isolated
// namespace. That also makes EraseSubject a namespace drop rather than a scan.

package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// BackendMessage is one opaque stored chat message. The backend persists Seq
// (the decorator-supplied ordering key), Role, Content and Timestamp verbatim
// and interprets none of them.
type BackendMessage struct {
	// Seq orders messages within (ns, session). The decorator allocates it; the
	// backend must return messages in ascending Seq order and must not reassign
	// it. It never decreases, even as older messages are pruned, so ordering
	// survives condensation.
	Seq uint64
	// Role is the speaker ("user" / "assistant"), opaque to the backend.
	Role string
	// Content is the message text, opaque to the backend — it is never indexed
	// or searched here (that is the semantic tier's job).
	Content string
	// Timestamp is when the message was recorded.
	Timestamp time.Time
}

// BackendEvent is one opaque pending consolidation intent — the outbox row.
//
// It carries the tenant/user/session identity in its own fields, and that is
// deliberate rather than a leak of the seal: the drain is CROSS-TENANT by
// design (one worker drains every namespace), so the decorator must be able to
// reconstruct each event's scope from the row itself. facts.DocBackend settled
// the same tension the same way — its ExportAll requires the backend to
// round-trip identity fields verbatim for the cross-tenant sleep-cycle path.
// The distinction that keeps the seal intact: the backend STORES these fields,
// it never DERIVES a namespace from them.
type BackendEvent struct {
	// ID is the idempotency key, derived by the decorator from the message
	// reference. Writing the same event twice must not produce two rows.
	ID        string
	Tenant    string
	User      string
	Session   string
	Content   string
	CreatedAt time.Time
	// Attempts and LastError carry the bounded-retry state (see
	// recordOutboxFailure). The backend round-trips them and applies no policy
	// of its own: the attempt budget and the dead-letter decision are the
	// decorator's.
	Attempts  int
	LastError string
	// Dead marks an event parked after exhausting its attempts. Parked events
	// are never returned by PendingEvents and are never deleted by the drain —
	// they are preserved until an operator replays them (RequeueDeadOutbox).
	Dead bool
}

// WorkingBackend is a tenant-unaware driver for the working tier: the active
// chat buffer, its condensed summary, and the transactional outbox.
//
// Every method takes an opaque `ns` the driver did not compose. Implementations
// MUST NOT attempt to parse, decode or correlate namespaces, and MUST NOT
// return data written under a different ns — that is an isolation breach the
// decorator cannot repair. The conformance suite
// (plugintest.RunWorkingBackendSuite) proves the seal against any
// implementation.
type WorkingBackend interface {
	// NextSeq returns a strictly increasing ordering value for (ns, session).
	// It is allocated OUTSIDE the atomic write below, so a failed write burns a
	// number; that is harmless, since the contract is uniqueness and ordering,
	// not density.
	NextSeq(ctx context.Context, ns, session string) (uint64, error)

	// AppendMessage records msg in (ns, session).
	AppendMessage(ctx context.Context, ns, session string, msg BackendMessage) error

	// AppendMessageWithEvent records msg AND event ATOMICALLY: both are durable
	// or neither is, even on a crash mid-commit.
	//
	// This is the constraint ADR-0081 names as the one that makes the port a
	// port and not a rename. The whole consolidation pipeline rests on it — lose
	// it and events are dropped or duplicated with no way to tell which. It is a
	// single interface method rather than two calls precisely because a
	// decorator CANNOT make two network round-trips atomic; only the driver can.
	// An implementation that cannot honour this must fail to construct rather
	// than silently degrade to two writes.
	AppendMessageWithEvent(ctx context.Context, ns, session string, msg BackendMessage, event BackendEvent) error

	// Recent returns up to n most recent messages in (ns, session), oldest
	// first. n <= 0 returns none.
	Recent(ctx context.Context, ns, session string, n int) ([]BackendMessage, error)

	// CountMessages returns how many active messages (ns, session) holds.
	CountMessages(ctx context.Context, ns, session string) (int, error)

	// OldestMessages returns up to n oldest messages in (ns, session), oldest
	// first — the condensation read.
	OldestMessages(ctx context.Context, ns, session string, n int) ([]BackendMessage, error)

	// Condense atomically replaces the summary of (ns, session) and removes the
	// messages with the given Seq values. Atomic for the same reason as
	// AppendMessageWithEvent: a summary written without its prune duplicates
	// content, and a prune without its summary loses it.
	Condense(ctx context.Context, ns, session, summary string, removeSeqs []uint64) error

	// GetSummary returns the condensed summary for (ns, session); ok is false
	// when none has been written.
	GetSummary(ctx context.Context, ns, session string) (string, bool, error)

	// SessionExists reports whether (ns, session) holds any message or summary.
	SessionExists(ctx context.Context, ns, session string) (bool, error)

	// DeleteNamespace removes EVERYTHING stored under ns — every session's
	// messages, summaries and outbox rows — and returns how many records were
	// removed. It backs EraseSubject, which is why it is a namespace drop
	// rather than a scan-and-filter: the erase must not be able to miss a key
	// shape the decorator forgot to enumerate.
	DeleteNamespace(ctx context.Context, ns string) (int, error)

	// PendingEvents returns every undelivered, un-parked outbox event across
	// ALL namespaces — the cross-tenant drain path, mirroring
	// facts.DocBackend.ExportAll. Parked (Dead) events are excluded.
	PendingEvents(ctx context.Context) ([]BackendEvent, error)

	// DeadEvents returns every parked event across ALL namespaces.
	DeadEvents(ctx context.Context) ([]BackendEvent, error)

	// PutEvent inserts or replaces an event by ID within ns — the retry-count
	// update, the park, and the requeue all land here.
	PutEvent(ctx context.Context, ns string, event BackendEvent) error

	// DeleteEvent removes the event keyed (ns, id). Unknown ids are ignored.
	DeleteEvent(ctx context.Context, ns, id string) error

	// Close releases the backend's resources.
	Close() error
}

// workingNamespaceVersion prefixes every derived namespace. It is versioned so
// a future change to the derivation is a visibly different namespace rather
// than a silent re-partition of live data.
const workingNamespaceVersion = "aisotropy-working-v1-"

// namespaceFor derives the opaque namespace for one (tenant, user) subject.
//
// UNEXPORTED, and that is the seal: a consumer holding the decorator cannot
// reach this function, cannot compose a namespace of its own, and cannot hand
// the backend one it chose. It mirrors facts.namespaceFor and
// semantic.namespaceFor byte-for-byte in construction — a SHA-256 over
// tenant\x00user — so the three tiers partition subjects identically and the
// separator makes ("a","bc") and ("ab","c") distinct namespaces rather than
// colliding ones.
//
// Session is deliberately NOT part of the namespace. The constitutional
// isolation boundary is (tenant, user); sessions are subdivisions inside an
// already-isolated namespace, which is what lets EraseSubject drop a namespace
// instead of enumerating every session that ever existed.
func namespaceFor(tenant, user string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + user))
	return workingNamespaceVersion + hex.EncodeToString(sum[:])
}
