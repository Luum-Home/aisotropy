package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNilWorkingBackend is returned by NewWorkingBackendStore when handed a nil
// backend: a clear error, never a store that silently drops every write.
var ErrNilWorkingBackend = errors.New("memory: working backend store requires a non-nil WorkingBackend")

// scopedWorkingStore wraps ANY tenant-unaware WorkingBackend into a store that
// satisfies the constitutional isolation gate.
//
// It is UNEXPORTED and constructed only via NewWorkingBackendStore: a consumer
// holds it as a *scopedWorkingStore obtained from that constructor and can
// never reach namespaceFor, never compose a namespace, and never hand the
// backend one of its own choosing. Every method derives the namespace from the
// caller-supplied Key and confines the backend call to it, so a cross-tenant
// read or write is not "prevented" by a check that could be forgotten — it is
// unrepresentable in the calls the backend receives.
//
// This mirrors facts.scopedDocStore and semantic.scopedVectorStore. The three
// tiers now seal identically, which is the point: an isolation property that is
// argued differently in three places is three properties.
type scopedWorkingStore struct {
	backend WorkingBackend
}

// NewWorkingBackendStore seals backend behind the tenant-isolation decorator.
//
// The returned value is the ONLY way to reach the backend through this package,
// and it never exposes the namespace it derives. Passing a backend that has
// already been handed out elsewhere defeats the seal — the caller must give up
// its reference.
func NewWorkingBackendStore(backend WorkingBackend) (*scopedWorkingStore, error) { //nolint:revive // unexported return is the seal: callers cannot construct or subtype it
	if backend == nil {
		return nil, ErrNilWorkingBackend
	}
	return &scopedWorkingStore{backend: backend}, nil
}

// ns derives the namespace for key's subject after validating the identity.
// Every method below goes through it, so an invalid identity can never reach
// the backend and no method can accidentally skip the derivation.
func (s *scopedWorkingStore) ns(key Key) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	return namespaceFor(key.Tenant, key.User), nil
}

// Append records msg in key's session.
func (s *scopedWorkingStore) Append(ctx context.Context, key Key, msg Message) error {
	ns, err := s.ns(key)
	if err != nil {
		return err
	}
	bm, err := s.prepare(ctx, ns, key, msg)
	if err != nil {
		return err
	}
	return s.backend.AppendMessage(ctx, ns, key.Session, bm)
}

// AppendWithOutbox records msg AND event atomically — the guarantee the whole
// consolidation pipeline rests on (ADR-0081's first constraint). It is ONE
// backend call, not two: a decorator cannot make two round-trips atomic, so the
// atomicity has to be the driver's to provide.
func (s *scopedWorkingStore) AppendWithOutbox(ctx context.Context, key Key, msg Message, event OutboxEvent) error {
	ns, err := s.ns(key)
	if err != nil {
		return err
	}
	bm, err := s.prepare(ctx, ns, key, msg)
	if err != nil {
		return err
	}
	return s.backend.AppendMessageWithEvent(ctx, ns, key.Session, bm, toBackendEvent(event))
}

// prepare allocates the ordering sequence and normalises the message.
func (s *scopedWorkingStore) prepare(ctx context.Context, ns string, key Key, msg Message) (BackendMessage, error) {
	if err := ctx.Err(); err != nil {
		return BackendMessage{}, err
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	seq, err := s.backend.NextSeq(ctx, ns, key.Session)
	if err != nil {
		return BackendMessage{}, fmt.Errorf("memory: allocate sequence: %w", err)
	}
	return BackendMessage{Seq: seq, Role: msg.Role, Content: msg.Content, Timestamp: msg.Timestamp}, nil
}

// Recent returns up to n most recent messages for key's session, oldest first.
func (s *scopedWorkingStore) Recent(ctx context.Context, key Key, n int) ([]Message, error) {
	ns, err := s.ns(key)
	if err != nil {
		return nil, err
	}
	raw, err := s.backend.Recent(ctx, ns, key.Session, n)
	if err != nil {
		return nil, err
	}
	return fromBackendMessages(raw), nil
}

// LookupSummary returns key's condensed summary and whether one exists.
func (s *scopedWorkingStore) LookupSummary(ctx context.Context, key Key) (string, bool, error) {
	ns, err := s.ns(key)
	if err != nil {
		return "", false, err
	}
	return s.backend.GetSummary(ctx, ns, key.Session)
}

// SessionExists reports whether key's session holds anything at all.
func (s *scopedWorkingStore) SessionExists(ctx context.Context, key Key) (bool, error) {
	ns, err := s.ns(key)
	if err != nil {
		return false, err
	}
	return s.backend.SessionExists(ctx, ns, key.Session)
}

// EraseSubject removes everything stored for (tenant, user) and returns how
// many records were removed.
//
// A namespace DROP, not a scan-and-filter, and the difference is a correctness
// property rather than a performance one: an erase built from an enumeration
// can only delete the key shapes whoever wrote it remembered to enumerate, so
// adding a new key shape silently makes the erase incomplete. Dropping the
// namespace cannot miss a shape it never had to know about.
func (s *scopedWorkingStore) EraseSubject(ctx context.Context, tenant, user string) (int, error) {
	if err := ValidateIdentityComponent("tenant", tenant); err != nil {
		return 0, err
	}
	if err := ValidateIdentityComponent("user", user); err != nil {
		return 0, err
	}
	return s.backend.DeleteNamespace(ctx, namespaceFor(tenant, user))
}

// PendingOutboxEvents returns every undelivered event across all subjects — the
// cross-tenant drain read. Each event's scope is reconstructed from the row's
// own identity fields, never from the namespace, which is why the backend must
// round-trip them verbatim.
func (s *scopedWorkingStore) PendingOutboxEvents(ctx context.Context) ([]OutboxEvent, error) {
	raw, err := s.backend.PendingEvents(ctx)
	if err != nil {
		return nil, err
	}
	return fromBackendEvents(raw), nil
}

// DeadOutboxEvents returns every parked event across all subjects.
func (s *scopedWorkingStore) DeadOutboxEvents(ctx context.Context) ([]OutboxEvent, error) {
	raw, err := s.backend.DeadEvents(ctx)
	if err != nil {
		return nil, err
	}
	return fromBackendEvents(raw), nil
}

// PutOutboxEvent writes event back — the attempt-count update, the park, and
// the requeue all land here. The namespace is re-derived from the event's own
// identity, so a caller cannot redirect an event into another subject's
// namespace by editing a string.
func (s *scopedWorkingStore) PutOutboxEvent(ctx context.Context, event OutboxEvent, dead bool) error {
	if err := ValidateIdentityComponent("tenant", event.Tenant); err != nil {
		return err
	}
	if err := ValidateIdentityComponent("user", event.User); err != nil {
		return err
	}
	be := toBackendEvent(event)
	be.Dead = dead
	return s.backend.PutEvent(ctx, namespaceFor(event.Tenant, event.User), be)
}

// DeleteOutboxEvent removes a delivered event.
func (s *scopedWorkingStore) DeleteOutboxEvent(ctx context.Context, event OutboxEvent) error {
	if err := ValidateIdentityComponent("tenant", event.Tenant); err != nil {
		return err
	}
	if err := ValidateIdentityComponent("user", event.User); err != nil {
		return err
	}
	return s.backend.DeleteEvent(ctx, namespaceFor(event.Tenant, event.User), event.ID)
}

// Close releases the backend.
func (s *scopedWorkingStore) Close() error { return s.backend.Close() }

func toBackendEvent(e OutboxEvent) BackendEvent {
	return BackendEvent{
		ID:        e.ID,
		Tenant:    e.Tenant,
		User:      e.User,
		Session:   e.Session,
		Content:   e.Content,
		CreatedAt: e.CreatedAt,
		Attempts:  e.Attempts,
		LastError: e.LastError,
	}
}

func fromBackendEvents(raw []BackendEvent) []OutboxEvent {
	out := make([]OutboxEvent, 0, len(raw))
	for _, b := range raw {
		out = append(out, OutboxEvent{
			ID:        b.ID,
			Tenant:    b.Tenant,
			User:      b.User,
			Session:   b.Session,
			Content:   b.Content,
			CreatedAt: b.CreatedAt,
			Attempts:  b.Attempts,
			LastError: b.LastError,
		})
	}
	return out
}

func fromBackendMessages(raw []BackendMessage) []Message {
	out := make([]Message, 0, len(raw))
	for _, b := range raw {
		out = append(out, Message{Role: b.Role, Content: b.Content, Timestamp: b.Timestamp})
	}
	return out
}
