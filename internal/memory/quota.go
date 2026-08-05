package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// This file implements per-tenant quota as a token bucket held in BadgerDB — the
// constitutional gate (.claude/rules/constitutional-gates.md): quota is checked
// BEFORE any LLM call so a tenant is rejected/degraded before it spends tokens.
//
// Because the working-memory BadgerDB runs with DetectConflicts=false (required
// by the async condensation path), we do NOT rely on transactional conflict
// detection to serialize the check-and-deduct. Instead the Limiter holds a
// per-tenant in-process mutex around each bucket update: correct for the
// embedded, per-instance store (every request for a tenant in this process goes
// through it), and different tenants proceed concurrently.
//
// Across replicas the bucket is NOT shared: each process owns its own embedded
// store. NewLimiterForReplicas divides the fleet-wide rate/burst by the declared
// replica count so the fleet honours the configured quota without putting a
// network round trip on the pre-LLM gate; read its doc for exactly what that
// approximation gives up. A truly shared bucket still belongs in the networked
// tier — the seam for it is Limiter itself (swap the badger-backed
// check-and-deduct, keep Allow's signature).

const quotaPrefix = "quota:"

// Limiter is a per-tenant token-bucket rate limiter backed by the working-memory
// BadgerDB. Construct it with WorkingMemory.NewLimiter so it shares the embedded
// store (µs, local) — no extra service.
type Limiter struct {
	db    *badger.DB
	rate  float64 // tokens refilled per second
	burst float64 // bucket capacity (max burst)
	now   func() time.Time

	mu    sync.Mutex             // guards locks
	locks map[string]*sync.Mutex // per-tenant serialization of check-and-deduct
}

// NewLimiter returns a token-bucket Limiter over this WorkingMemory's BadgerDB.
// ratePerSec is the sustained request rate; burst is the bucket capacity.
//
// The rate and burst are this PROCESS's bucket. In a multi-replica deployment
// use NewLimiterForReplicas so the fleet, not each replica, honours them.
func (w *WorkingMemory) NewLimiter(ratePerSec, burst float64) *Limiter {
	return w.NewLimiterForReplicas(ratePerSec, burst, 1)
}

// NewLimiterForReplicas returns a token-bucket Limiter whose per-process rate
// and burst are the FLEET-wide ratePerSec/burst divided by replicas — the
// static-division approximation to a shared quota.
//
// Why divide instead of sharing a bucket. Each replica owns its own embedded
// BadgerDB, so N replicas running NewLimiter(rate, burst) grant a tenant N x
// rate and N x burst: the constitutional pre-spend gate is silently N times
// looser than configured. A genuinely shared bucket needs a networked store on
// the request path — a round trip before every LLM call, on the hot path, for a
// decision that is already an approximation. Dividing keeps Allow at its
// measured ~7.7µs (quota_bench_test.go) and costs zero network calls.
//
// WHAT THIS APPROXIMATION LOSES, exactly:
//
//  1. SUSTAINED RATE is correct only under even distribution. The fleet refills
//     at ratePerSec in aggregate, but each replica refills at ratePerSec/N
//     independently. A tenant whose traffic lands entirely on one replica (a
//     sticky/hashing load balancer, a small fleet, an unlucky burst) is limited
//     to 1/N of its intended rate — this fails CLOSED (over-throttling), which
//     is the safe direction for a spend gate but is real under-service.
//  2. BURST is per replica. A tenant spreading requests across all N replicas
//     can still take burst/N tokens from each, so the fleet-wide instantaneous
//     burst is roughly the configured burst — but they arrive as N separate
//     bursts rather than one, so a downstream provider sees a spikier shape.
//  3. The bucket floor is NOT conservative. burst never drops below 1 token
//     (a replica that cannot serve a single request is useless), so a fleet
//     with replicas > burst grants up to N tokens instantaneously instead of
//     burst. Configure burst >= replicas to stay inside the intent.
//  4. UNUSED CAPACITY DOES NOT MOVE. An idle replica's tokens are not lent to a
//     saturated one; the fleet can deny a tenant while holding spare capacity.
//  5. Replica count is DECLARED, not observed. Scale up without updating it and
//     the fleet over-grants proportionally; scale down and it over-throttles.
//     Wire it to the same source of truth as the deployment's replica count.
//
// replicas <= 1 (the default, and any invalid value) leaves rate and burst
// exactly as given, so a single-process deployment is unchanged.
func (w *WorkingMemory) NewLimiterForReplicas(ratePerSec, burst float64, replicas int) *Limiter {
	if ratePerSec < 0 {
		ratePerSec = 0
	}
	if replicas > 1 {
		n := float64(replicas)
		ratePerSec /= n
		burst /= n
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{db: w.db, rate: ratePerSec, burst: burst, now: time.Now, locks: make(map[string]*sync.Mutex)}
}

// bucket is the persisted token-bucket state per tenant.
type bucket struct {
	Tokens       float64 `json:"tokens"`
	LastRefillNS int64   `json:"last_refill_ns"`
}

// tenantLock returns the per-tenant mutex, creating it on first use.
func (l *Limiter) tenantLock(tenant string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[tenant]
	if !ok {
		m = &sync.Mutex{}
		l.locks[tenant] = m
	}
	return m
}

// Allow attempts to take one token for tenant, returning true if the request is
// within quota. The check-refill-deduct is serialized per tenant, so concurrent
// callers can never collectively exceed the bucket.
func (l *Limiter) Allow(ctx context.Context, tenant string) (bool, error) {
	if tenant == "" {
		return false, errors.New("memory: quota requires a tenant")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	tl := l.tenantLock(tenant)
	tl.Lock()
	defer tl.Unlock()

	key := []byte(quotaPrefix + tenant)
	var allowed bool
	err := l.db.Update(func(txn *badger.Txn) error {
		b, err := l.load(txn, key)
		if err != nil {
			return err
		}
		now := l.now()
		tokens := b.Tokens
		if elapsed := time.Duration(now.UnixNano() - b.LastRefillNS); elapsed > 0 {
			tokens = min(l.burst, tokens+elapsed.Seconds()*l.rate)
		}
		if tokens < 1 {
			allowed = false
			return nil // denied: nothing to persist
		}
		b.Tokens = tokens - 1
		b.LastRefillNS = now.UnixNano()
		allowed = true
		payload, err := json.Marshal(b)
		if err != nil {
			return err
		}
		return txn.Set(key, payload)
	})
	if err != nil {
		return false, fmt.Errorf("memory: quota check: %w", err)
	}
	return allowed, nil
}

// load reads the tenant bucket, initializing a full one on first use.
func (l *Limiter) load(txn *badger.Txn, key []byte) (bucket, error) {
	item, err := txn.Get(key)
	switch {
	case errors.Is(err, badger.ErrKeyNotFound):
		return bucket{Tokens: l.burst, LastRefillNS: l.now().UnixNano()}, nil
	case err != nil:
		return bucket{}, err
	}
	var b bucket
	if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &b) }); err != nil {
		return bucket{}, err
	}
	return b, nil
}
