package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// ---- Multi-tenant scoped memory (memory_scoped, isolation_leak modes) ----
//
// Unlike the framework adapters — none of which has a native multi-tenant
// memory-isolation primitive, so each HAND-ROLLS a compound scope key
// (tenant_id, user_id, product_id[, session_id]) over a shared store and hopes
// nobody computes it wrong — aisotropy's isolation is BY CONSTRUCTION and needs
// NO adapter-side key or filter:
//
//   - Working memory (BadgerDB) keys every entry under the prefix
//     "tenant:{t}:user:{u}:session:{s}:..." (internal/memory/key.go). The ":"
//     separator is a RESERVED character rejected inside any identity component,
//     so "tenant:A:..." can never be a prefix of "tenant:B:..." — one tenant's
//     scan can never reach another's buffer. This is the enforcement point for
//     the constitutional multi-tenant isolation gate
//     (.claude/rules/constitutional-gates.md).
//   - The facts (rqlite/SQLite FTS5) and semantic (Qdrant) tiers are scoped from
//     the SAME ctx identity: Qdrant applies a tenant/user PAYLOAD FILTER DURING
//     the HNSW scan (not a post-filter), and facts are FTS5-scoped per tenant.
//
// Both modes drive the REAL runtime (App.Turn, full parity with
// POST /v1/tenant/{tenant_id}/user/{user_id}/chat): the fact is persisted as a
// real user turn under the caller's identity, and recall is a real turn whose
// hydration is bounded to that identity by the seams above. The adapter never
// looks at a key or filters a row — it simply asks aisotropy under a given
// identity and reports whether the scoped fact came back. That is the
// constitutional gate observed end to end.

const (
	// arenaScopeMinTimeout bounds one scoped turn (write or recall).
	arenaScopeTimeout = 110 * time.Second
)

// scopeIdentity resolves the (tenant, user, product) triple for a scoped mode
// from the request, falling back to the arena defaults only for product_id
// (tenant_id/user_id ARE the isolation identity and must be supplied explicitly).
func scopeIdentity(req *request) (tenant, user, product string) {
	tenant, user, product = req.TenantID, req.UserID, req.ProductID
	if product == "" {
		product = productID
	}
	return tenant, user, product
}

// validateScope enforces the non-empty identity the scoped modes require. It
// mirrors, at the adapter boundary, the same non-empty rule aisotropy's own
// memory.ValidateIdentityComponent applies deeper in — an empty identity is a
// misuse, never a silent cross-tenant read.
func validateScope(req *request) error {
	tenant, user, _ := scopeIdentity(req)
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("this mode requires a non-empty \"tenant_id\" (the isolation identity)")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("this mode requires a non-empty \"user_id\"")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("this mode requires a non-empty \"session_id\" (aisotropy's working memory is keyed tenant:{t}:user:{u}:session:{s}, so write and recall must share it)")
	}
	return nil
}

// scopedTurn runs exactly one App.Turn under the given identity and returns the
// reply, folding provider usage into out. It is the single shared path both
// modes use for writes and recalls, so the isolation behavior under test is
// identical across them.
func scopedTurn(req *request, out *result, prompt string) (string, error) {
	tenant, user, product := scopeIdentity(req)

	agnes, err := newAgnesClient()
	if err != nil {
		return "", err
	}
	app := newApp(agnes, req.System)
	defer func() { _ = app.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), arenaScopeTimeout)
	defer cancel()

	start := time.Now()
	o, terr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenant,
		UserID:    user,
		Prompt:    prompt,
		SessionID: req.SessionID,
		ProductID: product,
	})
	out.LatencyMs = time.Since(start).Milliseconds()
	setUsage(out, agnes)
	if terr != nil {
		return "", fmt.Errorf("App.Turn (tenant=%s user=%s session=%s): %w", tenant, user, req.SessionID, terr)
	}
	return o.Reply, nil
}

// runMemoryScoped persists a fact ("fact") or recalls it ("query") under the
// request's (tenant, user, product, session) identity, through aisotropy's real
// scoped working memory. Which operation runs is inferred from the payload.
//
//   - write: the fact is persisted as a real user turn (App.Turn ->
//     chatPersistUserTurn -> working memory, keyed by the identity's prefix).
//   - recall: a real turn whose hydration is bounded to the same identity. A
//     recall issued for the writing scope surfaces the fact (same_scope_hit); a
//     recall issued as a different tenant surfaces nothing, because that
//     tenant's key prefix never matches (cross_tenant_miss). The hit is read off
//     aisotropy's OWN reply (does it contain expect_substring?), never an
//     adapter-side lookup.
func runMemoryScoped(req *request, out *result) error {
	if err := validateScope(req); err != nil {
		return err
	}
	tenant, user, product := scopeIdentity(req)

	switch {
	case req.Fact != "" && req.Query != "":
		return fmt.Errorf("memory_scoped mode requires exactly one of \"fact\" (write) or \"query\" (recall), not both")

	case req.Fact != "":
		reply, err := scopedTurn(req, out, req.Fact)
		if err != nil {
			return err
		}
		out.Reply = reply
		logf("memory_scoped: persisted fact under tenant=%s user=%s product=%s session=%s (working-memory key-scoped by construction)", tenant, user, product, req.SessionID)
		return nil

	case req.Query != "":
		if req.ExpectSub == "" {
			return fmt.Errorf("memory_scoped recall requires \"expect_substring\" (the marker whose presence in aisotropy's reply proves the scoped fact was hydrated)")
		}
		reply, err := scopedTurn(req, out, req.Query)
		if err != nil {
			return err
		}
		hit := strings.Contains(reply, req.ExpectSub)
		out.Reply = reply
		out.ScopedRecall = &scopedRecall{
			SameScopeHit:    hit,
			CrossTenantMiss: !hit,
			RecalledReply:   reply,
		}
		logf("memory_scoped: recall as tenant=%s session=%s -> same_scope_hit=%v cross_tenant_miss=%v", tenant, req.SessionID, hit, !hit)
		return nil

	default:
		return fmt.Errorf("memory_scoped mode requires either \"fact\" (write) or \"query\" (recall)")
	}
}

// runIsolationLeak seeds a secret under one tenant's scope ("fact") or probes a
// scope for leakage ("query"), through the exact same real scoped working memory
// memory_scoped uses. The leak assertion is aisotropy's constitutional gate in
// action: a probe issued AS a different tenant surfaces nothing, because the
// working-memory key prefix + the ctx-scoped facts/semantic filters bound
// hydration to the probing identity — the adapter does NO filtering of its own.
// leak_detected=false means aisotropy's real reply, asked as tenant B, did not
// contain tenant A's secret.
func runIsolationLeak(req *request, out *result) error {
	if err := validateScope(req); err != nil {
		return err
	}
	tenant, _, _ := scopeIdentity(req)

	switch {
	case req.Fact != "" && req.Query != "":
		return fmt.Errorf("isolation_leak mode requires exactly one of \"fact\" (seed the secret) or \"query\" (probe), not both")

	case req.Fact != "":
		reply, err := scopedTurn(req, out, req.Fact)
		if err != nil {
			return err
		}
		out.Reply = reply
		logf("isolation_leak: seeded secret under tenant=%s session=%s", tenant, req.SessionID)
		return nil

	case req.Query != "":
		if req.ExpectSub == "" {
			return fmt.Errorf("isolation_leak probe requires \"expect_substring\" (the secret marker whose presence in the reply would be a leak)")
		}
		reply, err := scopedTurn(req, out, req.Query)
		if err != nil {
			return err
		}
		leaked := strings.Contains(reply, req.ExpectSub)
		out.Reply = reply
		out.LeakDetected = &leaked
		logf("isolation_leak: probe as tenant=%s session=%s -> leak_detected=%v", tenant, req.SessionID, leaked)
		return nil

	default:
		return fmt.Errorf("isolation_leak mode requires either \"fact\" (seed) or \"query\" (probe)")
	}
}
