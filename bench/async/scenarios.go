package asyncbench

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// GenEvents builds n outbox-shaped envelopes round-robined across tenants. ids
// are unique and stable so the oracle and broker-native dedup key off them.
func GenEvents(n int, tenants []string, prefix string) []Envelope {
	out := make([]Envelope, 0, n)
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		t := tenants[i%len(tenants)]
		out = append(out, Envelope{
			ID:        fmt.Sprintf("%s-%s-%06d", prefix, t, i),
			Tenant:    t,
			User:      "u1",
			Session:   "s1",
			Content:   fmt.Sprintf("turn %d for tenant %s: remember this fact", i, t),
			CreatedAt: now.Add(time.Duration(i) * time.Nanosecond),
		})
	}
	return out
}

// Result is the full per-broker measured outcome.
type Result struct {
	Broker  string
	License string

	// Latency + throughput (happy path).
	Latency             LatencyReport
	Published           int
	Consumed            int
	ThroughputMsgPerSec float64

	// Delivery-under-failure.
	Redelivery RedeliveryResult

	// Tenant isolation.
	Isolation IsolationResult

	Err error // fatal error that aborted this broker's run
}

type RedeliveryResult struct {
	Published        int
	UniqueDelivered  int  // oracle facts
	TotalDeliveries  int  // oracle deliveries
	Duplicates       int  // redeliveries absorbed
	AllDelivered     bool // every published id eventually delivered (at-least-once)
	NoDuplicateFacts bool // facts == published (idempotency held)
	RedeliveryProven bool // duplicates > 0 (a redelivery actually occurred)
	Note             string
}

type IsolationResult struct {
	TenantA    string
	TenantB    string
	AReceived  int
	BLeakedToA int  // MUST be 0
	Isolated   bool // BLeakedToA == 0
}

// RunLatencyThroughput drives the happy path: a consumer per tenant drains while
// the relay publishes all events, measuring end-to-end latency and sustained
// msg/s. It returns when every published id has been consumed+acked or ctx ends.
func RunLatencyThroughput(ctx context.Context, b Broker, tenants []string, group string, n, publishWorkers int) (LatencyReport, int, int, float64, error) {
	events := GenEvents(n, tenants, "lt")
	lat := NewLatencies()
	oracle := NewOracle()

	var consumed int64
	done := make(chan struct{})
	var once sync.Once
	signalDone := func() { once.Do(func() { close(done) }) }

	closers := make([]interface{ Close() error }, 0, len(tenants))
	for _, t := range tenants {
		onMsg := func(e Envelope) bool {
			ack := time.Now()
			if oracle.Consolidate(e.ID) {
				lat.Add(LatencyOf(e, ack))
			}
			if atomic.AddInt64(&consumed, 1) >= int64(n) {
				signalDone()
			}
			return true // ack
		}
		c, err := b.StartConsumer(ctx, t, group, onMsg)
		if err != nil {
			return LatencyReport{}, 0, 0, 0, fmt.Errorf("start consumer %s: %w", t, err)
		}
		closers = append(closers, c)
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	relay := NewRelay(b)
	src := NewOutboxSource(events)
	published, elapsed, err := relay.Drain(ctx, src, publishWorkers)
	if err != nil {
		return LatencyReport{}, published, int(atomic.LoadInt64(&consumed)), 0, err
	}

	// Wait for consumption to catch up (bounded).
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
	}
	// Throughput = confirmed publishes over the drain window (the rate an
	// at-least-once outbox relay can actually hand off, each publish confirmed).
	tput := float64(published) / elapsed.Seconds()
	return lat.Report(), published, int(atomic.LoadInt64(&consumed)), tput, nil
}

// RunRedelivery proves at-least-once under a mid-batch consumer crash. It:
//  1. publishes n events for one tenant,
//  2. runs consumer #1 that ACKS only the first `ackCut` it sees, then is
//     hard-closed (unacked in-flight left dangling),
//  3. runs consumer #2 that acks everything,
//  4. asserts every id was delivered (at-least-once) with no duplicate facts,
//     and that at least one redelivery actually occurred.
func RunRedelivery(ctx context.Context, b Broker, tenant, group string, n int) (RedeliveryResult, error) {
	events := GenEvents(n, []string{tenant}, "rd")
	oracle := NewOracle()

	// Publish everything first so the crash happens against a full backlog.
	relay := NewRelay(b)
	published, _, err := relay.Drain(ctx, NewOutboxSource(events), 4)
	if err != nil {
		return RedeliveryResult{}, err
	}

	// Consumer #1: ack a prefix, then refuse (return false) so those stay
	// unacked/redeliverable, then we hard-close it.
	var acked int64
	ackCut := int64(n / 2)
	crash := make(chan struct{})
	var crashOnce sync.Once
	c1, err := b.StartConsumer(ctx, tenant, group, func(e Envelope) bool {
		oracle.Consolidate(e.ID)
		if atomic.AddInt64(&acked, 1) <= ackCut {
			return true // ack the prefix
		}
		crashOnce.Do(func() { close(crash) })
		return false // refuse: leave unacked -> redelivery
	})
	if err != nil {
		return RedeliveryResult{}, fmt.Errorf("consumer#1: %w", err)
	}
	select {
	case <-crash:
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
	}
	_ = c1.Close() // hard kill: in-flight unacked messages are abandoned

	// Give the broker time to move unacked -> redeliverable (ack-wait / PEL / uncommitted).
	time.Sleep(3 * time.Second)

	// Consumer #2: drain the remainder + any redelivered, ack all.
	remaining := make(chan struct{})
	var remOnce sync.Once
	c2, err := b.StartConsumer(ctx, tenant, group, func(e Envelope) bool {
		oracle.Consolidate(e.ID)
		if oracle.Facts() >= published {
			remOnce.Do(func() { close(remaining) })
		}
		return true
	})
	if err != nil {
		return RedeliveryResult{}, fmt.Errorf("consumer#2: %w", err)
	}
	select {
	case <-remaining:
	case <-time.After(20 * time.Second):
	case <-ctx.Done():
	}
	_ = c2.Close()

	res := RedeliveryResult{
		Published:        published,
		UniqueDelivered:  oracle.Facts(),
		TotalDeliveries:  oracle.Deliveries(),
		Duplicates:       oracle.Duplicates(),
		AllDelivered:     oracle.Facts() >= published,
		NoDuplicateFacts: oracle.Facts() == published,
		RedeliveryProven: oracle.Duplicates() > 0,
	}
	if !res.RedeliveryProven {
		res.Note = "no duplicate observed — either all acked before crash or broker redelivered cleanly to a fresh read; at-least-once still holds if AllDelivered"
	}
	return res, nil
}

// RunIsolation proves cross-tenant isolation: publish to A and B, subscribe a
// consumer to A only, assert it never sees a B envelope.
func RunIsolation(ctx context.Context, b Broker, tenantA, tenantB, group string, perTenant int) (IsolationResult, error) {
	evA := GenEvents(perTenant, []string{tenantA}, "iso")
	evB := GenEvents(perTenant, []string{tenantB}, "iso")

	oracleA := NewOracle()
	var aRecv, bLeak int64

	cA, err := b.StartConsumer(ctx, tenantA, group, func(e Envelope) bool {
		oracleA.Consolidate(e.ID)
		if e.Tenant == tenantB {
			atomic.AddInt64(&bLeak, 1)
		} else {
			atomic.AddInt64(&aRecv, 1)
		}
		return true
	})
	if err != nil {
		return IsolationResult{}, err
	}
	defer cA.Close()

	relay := NewRelay(b)
	if _, _, err := relay.Drain(ctx, NewOutboxSource(evA), 2); err != nil {
		return IsolationResult{}, err
	}
	if _, _, err := relay.Drain(ctx, NewOutboxSource(evB), 2); err != nil {
		return IsolationResult{}, err
	}

	// Wait until A's full set is in, then give any (illegal) B leak a chance to arrive.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&aRecv) >= int64(perTenant) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // settle window to catch a stray B

	return IsolationResult{
		TenantA:    tenantA,
		TenantB:    tenantB,
		AReceived:  int(atomic.LoadInt64(&aRecv)),
		BLeakedToA: int(atomic.LoadInt64(&bLeak)),
		Isolated:   atomic.LoadInt64(&bLeak) == 0,
	}, nil
}
