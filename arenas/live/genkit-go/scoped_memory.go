package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// This file adds two modes on top of the plain "memory" mode (memory.go):
// "memory" keys state by session_id ALONE, so it proves cross-process
// persistence but says nothing about multi-tenant isolation. Genkit-Go has
// NO native multi-tenant memory-isolation primitive — no scoped store, no
// tenant-aware retriever contract, nothing. This file hand-rolls a COMPOUND
// scope key (tenant_id, user_id, product_id[, session_id]) on top of the
// same rqlite store memory.go already uses, and the dev wiring this adapter
// is fully responsible for supplying every scope column on every read and
// write — get it wrong (e.g. drop tenant_id from a WHERE clause) and it is a
// silent cross-tenant data leak, not a framework error. See README.md
// "memory_scoped / isolation_leak" for the full honest note and the
// contrast with aisotropy's constructional tenant isolation
// (.claude/rules/constitutional-gates.md "Multi-tenant data isolation").

// defaultCrossTenantID returns supplied if non-empty, otherwise synthesizes a
// "different tenant B" probe id guaranteed to differ from tenantID.
func defaultCrossTenantID(tenantID, supplied string) string {
	if supplied != "" {
		return supplied
	}
	return tenantID + "-cross-tenant-probe"
}

// scopeKey renders the compound scope as a single string for PROGRESS
// logging only. The actual isolation boundary is the four separate SQL
// WHERE-clause columns in rqlite_client.go's recallScopedFacts/
// writeScopedFact — never this string (a real implementation must not
// collapse the scope into one opaque key and match on that; doing so is
// exactly the kind of shortcut that turns into a leak once two different
// (tenant,user,product) tuples happen to render to related strings).
func scopeKey(tenantID, userID, productID, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s:%s", tenantID, userID, productID, sessionID)
}

// runMemoryScoped proves the hand-rolled compound-scope store end to end in
// one process invocation:
//  1. WRITE rc.in.Fact under scope (tenant_id, user_id, product_id[, session_id]).
//  2. RECALL under the SAME scope — expect the fact back (same_scope_hit).
//  3. RECALL under a DIFFERENT tenant (same user/product/session) — expect
//     nothing back (cross_tenant_miss).
//
// A single real agnes call then answers rc.in.Query grounded ONLY in
// whatever the same-scope recall returned, so the recall path is proven to
// feed a real generation turn, not just a SQL read nothing consumes.
func runMemoryScoped(rc *runCtx, out *arenaOutput) error {
	if rc.in.TenantID == "" || rc.in.UserID == "" || rc.in.ProductID == "" {
		return fmt.Errorf("memory_scoped mode: tenant_id, user_id and product_id are all required")
	}
	if rc.in.Fact == "" {
		return fmt.Errorf("memory_scoped mode: fact is required (the fact to write and recall)")
	}
	query := rc.in.Query
	if query == "" {
		query = "What fact did I ask you to remember?"
	}
	crossTenantID := defaultCrossTenantID(rc.in.TenantID, rc.in.CrossTenantID)
	if crossTenantID == rc.in.TenantID {
		return fmt.Errorf("memory_scoped mode: cross_tenant_id must differ from tenant_id")
	}

	rq := newRqliteClient(rc.rqliteURL)
	if err := rq.ensureScopedTable(rc.ctx); err != nil {
		return fmt.Errorf("memory_scoped: ensure table: %w", err)
	}

	start := time.Now()

	fmt.Fprintf(os.Stderr, "PROGRESS: memory_scoped: writing fact under scope %q\n",
		scopeKey(rc.in.TenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID))
	if err := rq.writeScopedFact(rc.ctx, rc.in.TenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID, rc.in.Fact); err != nil {
		return fmt.Errorf("memory_scoped: write fact: %w", err)
	}

	sameScopeFacts, err := rq.recallScopedFacts(rc.ctx, rc.in.TenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID)
	if err != nil {
		return fmt.Errorf("memory_scoped: recall same scope: %w", err)
	}
	sameScopeHit := len(sameScopeFacts) > 0
	recalledText := ""
	if sameScopeHit {
		recalledText = sameScopeFacts[0]
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: memory_scoped: same-scope recall returned %d fact(s), hit=%v\n", len(sameScopeFacts), sameScopeHit)

	crossFacts, err := rq.recallScopedFacts(rc.ctx, crossTenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID)
	if err != nil {
		return fmt.Errorf("memory_scoped: recall cross-tenant scope: %w", err)
	}
	crossTenantMiss := len(crossFacts) == 0
	fmt.Fprintf(os.Stderr, "PROGRESS: memory_scoped: cross-tenant recall (tenant=%q) returned %d fact(s), miss=%v\n", crossTenantID, len(crossFacts), crossTenantMiss)

	system := "You are a precise assistant. Answer ONLY using the context below. " +
		"If the context is empty or does not contain the answer, say plainly that you have no information — never invent an answer.\n\n" +
		"CONTEXT:\n" + strings.Join(sameScopeFacts, "\n")

	resp, err := genkit.Generate(rc.ctx, rc.g,
		ai.WithModel(rc.model),
		ai.WithSystem(system),
		ai.WithPrompt(query),
	)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("memory_scoped: genkit.Generate: %w", err)
	}
	applyUsage(out, resp)
	out.Reply = resp.Text()
	out.ScopedRecall = &scopedRecallInfo{
		SameScopeHit:    sameScopeHit,
		CrossTenantMiss: crossTenantMiss,
		RecalledText:    recalledText,
	}
	return nil
}

// runIsolationLeak proves the same hand-rolled scope store does NOT leak a
// secret across tenants: tenant A (rc.in.TenantID) writes a secret
// (rc.in.Fact); the store is then queried AS tenant B (same
// user/product/session) and a real agnes turn answers rc.in.Query grounded
// ONLY in whatever that tenant-B-scoped recall returned.
//
// leak_detected is true if EITHER (a) the scoped store itself returned
// tenant A's secret under tenant B's scope, or (b) the grounded reply
// nonetheless contains the secret text. (a) is the primary, authoritative
// assertion — per the isolation contract, a correct scope key structurally
// starves the model of the secret before generation even happens, so a
// clean store recall should make (b) impossible by construction. (b) is a
// belt-and-suspenders second signal, not a substitute for (a): "the
// isolation assertion is on what the scoped store returns."
func runIsolationLeak(rc *runCtx, out *arenaOutput) error {
	if rc.in.TenantID == "" || rc.in.UserID == "" || rc.in.ProductID == "" {
		return fmt.Errorf("isolation_leak mode: tenant_id, user_id and product_id are all required (tenant A, the writer)")
	}
	if rc.in.Fact == "" {
		return fmt.Errorf("isolation_leak mode: fact is required (the secret tenant A writes)")
	}
	query := rc.in.Query
	if query == "" {
		query = "What secret did I tell you?"
	}
	tenantB := defaultCrossTenantID(rc.in.TenantID, rc.in.CrossTenantID)
	if tenantB == rc.in.TenantID {
		return fmt.Errorf("isolation_leak mode: cross_tenant_id must differ from tenant_id")
	}

	rq := newRqliteClient(rc.rqliteURL)
	if err := rq.ensureScopedTable(rc.ctx); err != nil {
		return fmt.Errorf("isolation_leak: ensure table: %w", err)
	}

	start := time.Now()

	fmt.Fprintf(os.Stderr, "PROGRESS: isolation_leak: tenant %q writes a secret under scope %q\n",
		rc.in.TenantID, scopeKey(rc.in.TenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID))
	if err := rq.writeScopedFact(rc.ctx, rc.in.TenantID, rc.in.UserID, rc.in.ProductID, rc.in.SessionID, rc.in.Fact); err != nil {
		return fmt.Errorf("isolation_leak: write secret: %w", err)
	}

	// The isolation assertion: query the SAME store AS tenant B.
	tenantBFacts, err := rq.recallScopedFacts(rc.ctx, tenantB, rc.in.UserID, rc.in.ProductID, rc.in.SessionID)
	if err != nil {
		return fmt.Errorf("isolation_leak: recall as tenant B: %w", err)
	}
	storeLeaked := false
	for _, f := range tenantBFacts {
		if strings.Contains(f, rc.in.Fact) {
			storeLeaked = true
			break
		}
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: isolation_leak: tenant %q recall returned %d fact(s), store_leaked=%v\n", tenantB, len(tenantBFacts), storeLeaked)

	// A real agnes recall turn AS tenant B, grounded ONLY in whatever the
	// store returned for tenant B's scope (normally nothing).
	system := "You are a precise assistant. Answer ONLY using the context below, which is everything you know for THIS user. " +
		"If the context is empty or does not contain the answer, say plainly that you have no information — never invent an answer.\n\n" +
		"CONTEXT:\n" + strings.Join(tenantBFacts, "\n")

	resp, err := genkit.Generate(rc.ctx, rc.g,
		ai.WithModel(rc.model),
		ai.WithSystem(system),
		ai.WithPrompt(query),
	)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("isolation_leak: genkit.Generate: %w", err)
	}
	applyUsage(out, resp)
	out.Reply = resp.Text()

	replyLeaked := strings.Contains(out.Reply, rc.in.Fact)
	if replyLeaked && !storeLeaked {
		fmt.Fprintf(os.Stderr, "PROGRESS: isolation_leak: WARNING reply-level leak with a clean store recall — investigate separately from the store-level assertion\n")
	}
	leakDetected := storeLeaked || replyLeaked
	out.LeakDetected = &leakDetected
	return nil
}
