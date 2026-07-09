package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// This file adds a read-only view of quota pressure on top of the Limiter
// defined in quota.go, in support of Phase 7 graceful degradation: instead of
// hard-rejecting a tenant at 100% of its bucket (Allow's 429 behavior), the
// caller can inspect Pressure and route to a cheaper model as the tenant
// approaches its limit (see internal/llm/degrade.go for the policy that
// consumes this value).

// Pressure reports how close tenant is to exhausting its token bucket, as a
// fraction in [0, 1].
//
// Convention: pressure is the fraction of burst CONSUMED — i.e.
// pressure = 1 - tokens/burst. A fresh or fully-refilled bucket (tokens ==
// burst) has pressure 0 (no pressure, plenty of headroom). A fully-drained
// bucket (tokens == 0) has pressure 1 (at the limit). This matches the
// natural reading of a "pressure" threshold: DegradeAt(pressure, 0.95, ...)
// degrades once 95% of the burst has been consumed.
//
// Pressure PEEKS the bucket: it applies the same time-based refill
// computation as Allow (elapsed time since the last refill times rate,
// capped at burst) but never writes the recomputed state back and never
// deducts a token. It takes the same per-tenant lock Allow uses so the read
// is consistent with any concurrent check-and-deduct, but since it only
// reads, it does not serialize against other Pressure calls' correctness —
// only against torn reads of a concurrent Allow.
func (l *Limiter) Pressure(ctx context.Context, tenant string) (float64, error) {
	if tenant == "" {
		return 0, errors.New("memory: quota requires a tenant")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	tl := l.tenantLock(tenant)
	tl.Lock()
	defer tl.Unlock()

	key := []byte(quotaPrefix + tenant)
	var pressure float64
	err := l.db.View(func(txn *badger.Txn) error {
		b, err := l.load(txn, key)
		if err != nil {
			return err
		}
		tokens := b.Tokens
		now := l.now()
		if elapsed := time.Duration(now.UnixNano() - b.LastRefillNS); elapsed > 0 {
			tokens = min(l.burst, tokens+elapsed.Seconds()*l.rate)
		}
		if l.burst <= 0 {
			pressure = 1 // degenerate bucket: treat as fully consumed
			return nil
		}
		pressure = 1 - tokens/l.burst
		switch {
		case pressure < 0:
			pressure = 0
		case pressure > 1:
			pressure = 1
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("memory: quota pressure: %w", err)
	}
	return pressure, nil
}
