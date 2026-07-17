package asyncbench

import "sync"

// Oracle is the idempotency oracle: the consumer-side stand-in for the real
// facts+semantic sinks, whose ConsolidationSink contract is documented as
// "MUST be idempotent: delivery is at-least-once" (internal/memory/outbox.go).
//
// It records, per envelope ID, whether a fact was already produced. A first
// sighting produces a fact; every later sighting of the same ID is a duplicate
// delivery that MUST NOT produce a second fact. After a run:
//
//	Facts()       == number of UNIQUE ids  (no duplicate facts)
//	Deliveries()  >= Facts()               (>-strictly proves redelivery happened)
//	Duplicates()  == Deliveries() - Facts()
type Oracle struct {
	mu         sync.Mutex
	seen       map[string]int // id -> times delivered
	deliveries int
}

func NewOracle() *Oracle {
	return &Oracle{seen: make(map[string]int)}
}

// Consolidate applies one delivery of id. Returns true if this delivery created
// a NEW fact (first sighting), false if it was a duplicate (idempotent no-op).
func (o *Oracle) Consolidate(id string) (created bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deliveries++
	n := o.seen[id]
	o.seen[id]++
	return n == 0
}

// Facts is the number of unique ids consolidated (== number of facts created).
func (o *Oracle) Facts() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

// Deliveries is the total number of Consolidate calls (unique + duplicates).
func (o *Oracle) Deliveries() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.deliveries
}

// Duplicates is Deliveries - Facts: how many redeliveries the oracle absorbed.
func (o *Oracle) Duplicates() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.deliveries - len(o.seen)
}

// Has reports whether id was ever delivered — used by the tenant-isolation
// assertion (a cross-tenant id must NEVER appear).
func (o *Oracle) Has(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.seen[id]
	return ok
}
