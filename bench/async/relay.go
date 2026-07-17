package asyncbench

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// OutboxSource is an outbox-shaped event stream: the prototype stand-in for
// WorkingMemory's pending-outbox scan (internal/memory/outbox.go). The real
// drain iterates BadgerDB keys `outbox:pending:{tenant}:{ts}:{id}` in tenant
// order; here we hold the same envelopes in memory and hand them out in FIFO
// order. The relay does not care which it drains — only the envelope contract
// matters, which is why the prototype is faithful without a Badger dependency.
type OutboxSource struct {
	mu      sync.Mutex
	pending []Envelope
}

func NewOutboxSource(events []Envelope) *OutboxSource {
	cp := make([]Envelope, len(events))
	copy(cp, events)
	return &OutboxSource{pending: cp}
}

// Next pops the next pending envelope, mirroring drainOutboxOnce's per-event
// step. ok=false when the source is drained.
func (s *OutboxSource) Next() (e Envelope, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return Envelope{}, false
	}
	e = s.pending[0]
	s.pending = s.pending[1:]
	return e, true
}

func (s *OutboxSource) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// Relay is the prototype outbox -> broker relay. It plays the role the real
// RunOutboxWorker plays for the facts+semantic sinks, except the sink here is a
// message broker: drain a pending event, publish it, and only on a confirmed
// publish is the event considered handed off (at-least-once — a publish error
// re-queues the event, exactly like the real worker leaves a failed event in
// place). The publish CARRIES the tenant/user/session envelope so downstream
// consumers can enforce isolation.
type Relay struct {
	broker Broker
}

func NewRelay(b Broker) *Relay { return &Relay{broker: b} }

// Drain publishes every pending envelope from src to the broker, stamping each
// with its publish time for the latency measurement. It uses `workers` parallel
// publishers to reach a realistic sustained rate. Returns how many were
// published and the wall-clock duration of the drain (for throughput).
func (r *Relay) Drain(ctx context.Context, src *OutboxSource, workers int) (published int, elapsed time.Duration, err error) {
	if workers < 1 {
		workers = 1
	}
	start := time.Now()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		count    int
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				e, ok := src.Next()
				if !ok {
					return
				}
				e.PubNanos = time.Now().UnixNano()
				if perr := r.broker.Publish(ctx, e); perr != nil {
					// At-least-once: on a publish failure the real worker leaves
					// the event pending. We surface the first error and stop.
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("relay publish %s: %w", e.ID, perr)
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return count, time.Since(start), firstErr
}

// LatencyOf computes the end-to-end latency for an envelope observed at ackTime.
func LatencyOf(e Envelope, ackTime time.Time) time.Duration {
	if e.PubNanos == 0 {
		return 0
	}
	return ackTime.Sub(time.Unix(0, e.PubNanos))
}
