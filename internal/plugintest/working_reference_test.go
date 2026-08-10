package plugintest

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// refWorkingBackend is an in-memory reference implementation of
// memory.WorkingBackend. It exists to run the conformance suite against
// something — a suite nobody has ever seen pass is not evidence that the
// contract is satisfiable, only that it compiles.
//
// It is deliberately dumb: maps and a mutex, no indexes, no cleverness. Its job
// is to be obviously correct, so a suite failure indicts the suite or the
// contract rather than this file.
type refWorkingBackend struct {
	mu sync.Mutex
	// messages[ns][session] holds the active buffer, kept sorted by Seq.
	messages map[string]map[string][]memory.BackendMessage
	summary  map[string]map[string]string
	seq      map[string]uint64
	// events[ns][id] holds both pending and parked events; Dead distinguishes.
	events map[string]map[string]memory.BackendEvent
}

func newRefWorkingBackend() memory.WorkingBackend {
	return &refWorkingBackend{
		messages: map[string]map[string][]memory.BackendMessage{},
		summary:  map[string]map[string]string{},
		seq:      map[string]uint64{},
		events:   map[string]map[string]memory.BackendEvent{},
	}
}

// Namespaces implements RawWorkingInspector so the suite can prove the seal.
func (b *refWorkingBackend) Namespaces() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[string]bool{}
	for ns := range b.messages {
		seen[ns] = true
	}
	for ns := range b.events {
		seen[ns] = true
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (b *refWorkingBackend) NextSeq(_ context.Context, ns, session string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := ns + "\x00" + session
	b.seq[k]++
	return b.seq[k], nil
}

func (b *refWorkingBackend) AppendMessage(_ context.Context, ns, session string, msg memory.BackendMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appendLocked(ns, session, msg)
	return nil
}

func (b *refWorkingBackend) AppendMessageWithEvent(_ context.Context, ns, session string, msg memory.BackendMessage, event memory.BackendEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// One critical section: both mutations are visible together or not at all.
	// An in-memory map cannot demonstrate crash-atomicity, and the suite does
	// not claim it does.
	b.appendLocked(ns, session, msg)
	b.putEventLocked(ns, event)
	return nil
}

func (b *refWorkingBackend) appendLocked(ns, session string, msg memory.BackendMessage) {
	if b.messages[ns] == nil {
		b.messages[ns] = map[string][]memory.BackendMessage{}
	}
	s := append(b.messages[ns][session], msg)
	sort.Slice(s, func(i, j int) bool { return s[i].Seq < s[j].Seq })
	b.messages[ns][session] = s
}

func (b *refWorkingBackend) putEventLocked(ns string, event memory.BackendEvent) {
	if b.events[ns] == nil {
		b.events[ns] = map[string]memory.BackendEvent{}
	}
	b.events[ns][event.ID] = event // keyed by id: idempotent by construction
}

func (b *refWorkingBackend) Recent(_ context.Context, ns, session string, n int) ([]memory.BackendMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	all := b.messages[ns][session]
	if n <= 0 || len(all) == 0 {
		return nil, nil
	}
	// Take the NEWEST n, returned oldest-first.
	if n > len(all) {
		n = len(all)
	}
	out := make([]memory.BackendMessage, n)
	copy(out, all[len(all)-n:])
	return out, nil
}

func (b *refWorkingBackend) CountMessages(_ context.Context, ns, session string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages[ns][session]), nil
}

func (b *refWorkingBackend) OldestMessages(_ context.Context, ns, session string, n int) ([]memory.BackendMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	all := b.messages[ns][session]
	if n <= 0 || len(all) == 0 {
		return nil, nil
	}
	if n > len(all) {
		n = len(all)
	}
	out := make([]memory.BackendMessage, n)
	copy(out, all[:n])
	return out, nil
}

func (b *refWorkingBackend) Condense(_ context.Context, ns, session, summary string, removeSeqs []uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	drop := map[uint64]bool{}
	for _, s := range removeSeqs {
		drop[s] = true
	}
	kept := b.messages[ns][session][:0]
	for _, m := range b.messages[ns][session] {
		if !drop[m.Seq] {
			kept = append(kept, m)
		}
	}
	if b.messages[ns] == nil {
		b.messages[ns] = map[string][]memory.BackendMessage{}
	}
	b.messages[ns][session] = kept
	if b.summary[ns] == nil {
		b.summary[ns] = map[string]string{}
	}
	b.summary[ns][session] = summary
	return nil
}

func (b *refWorkingBackend) GetSummary(_ context.Context, ns, session string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.summary[ns][session]
	return s, ok, nil
}

func (b *refWorkingBackend) SessionExists(_ context.Context, ns, session string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.messages[ns][session]) > 0 {
		return true, nil
	}
	_, ok := b.summary[ns][session]
	return ok, nil
}

func (b *refWorkingBackend) DeleteNamespace(_ context.Context, ns string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, msgs := range b.messages[ns] {
		n += len(msgs)
	}
	n += len(b.summary[ns]) + len(b.events[ns])
	delete(b.messages, ns)
	delete(b.summary, ns)
	delete(b.events, ns)
	for k := range b.seq {
		if len(k) > len(ns) && k[:len(ns)] == ns && k[len(ns)] == 0 {
			delete(b.seq, k)
		}
	}
	return n, nil
}

func (b *refWorkingBackend) PendingEvents(_ context.Context) ([]memory.BackendEvent, error) {
	return b.collectEvents(false), nil
}

func (b *refWorkingBackend) DeadEvents(_ context.Context) ([]memory.BackendEvent, error) {
	return b.collectEvents(true), nil
}

func (b *refWorkingBackend) collectEvents(dead bool) []memory.BackendEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []memory.BackendEvent
	for _, byID := range b.events {
		for _, e := range byID {
			if e.Dead == dead {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *refWorkingBackend) PutEvent(_ context.Context, ns string, event memory.BackendEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.putEventLocked(ns, event)
	return nil
}

func (b *refWorkingBackend) DeleteEvent(_ context.Context, ns, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.events[ns], id) // unknown id is a no-op, per the contract
	return nil
}

func (b *refWorkingBackend) Close() error { return nil }

// TestReferenceWorkingBackendConformance runs the ADR-0081 working-tier suite
// against the reference implementation.
func TestReferenceWorkingBackendConformance(t *testing.T) {
	RunWorkingBackendSuite(t, newRefWorkingBackend)
}
