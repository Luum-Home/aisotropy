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
// through it), and different tenants proceed concurrently. Shared quota across
// replicas would move this state to the networked tier — out of scope here.

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
func (w *WorkingMemory) NewLimiter(ratePerSec, burst float64) *Limiter {
	if ratePerSec < 0 {
		ratePerSec = 0
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
