// Offline smoke test for the Eino Orbit arena.
//
// Everything below runs through the REAL CloudWeGo Eino runtime
// (github.com/cloudwego/eino v0.9.x) with a stub ChatModel — NO API key.
//
//	Part 1: a real Eino chain chat turn + real streaming + real ReAct tool call
//	Part 2: the adversarial isolation matrix against Eino's REAL checkpoint/state
//	        model (compose.CheckPointStore + compose.WithCheckPointID)
//
// Run: go run ./scripts/smoke.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"eino-arena/orbit"
)

func main() {
	ctx := context.Background()
	line := strings.Repeat("=", 64)
	fmt.Println(line)
	fmt.Println("Eino Orbit — offline smoke (REAL github.com/cloudwego/eino, stub model)")
	fmt.Println(line)

	ok := true
	ok = part1Chatbot(ctx) && ok
	fmt.Println()
	ok = part2Isolation(ctx) && ok

	fmt.Println()
	fmt.Println(line)
	if ok {
		fmt.Println("SMOKE RESULT: PASS — real Eino stack exercised; isolation matrix behaved as classified.")
		return
	}
	fmt.Println("SMOKE RESULT: FAIL — see markers above.")
	os.Exit(1)
}

// part1Chatbot drives Orbit's real Eino chain, streaming, and ReAct agent.
func part1Chatbot(ctx context.Context) bool {
	fmt.Println("\n[Part 1] Orbit chatbot on the real Eino runtime")
	fmt.Println(strings.Repeat("-", 64))

	ob, err := orbit.New(ctx)
	if err != nil {
		fmt.Println("  ✗ orbit.New failed:", err)
		return false
	}
	pass := true

	// 1a. Plain chat turn through a compiled compose.Chain.
	ob.Remember("acme", "alice", "s1", "prefers email over phone")
	reply, err := ob.Chat(ctx, "acme", "alice", "s1", "How do I reset my password?")
	if err != nil {
		fmt.Println("  ✗ Chat error:", err)
		pass = false
	} else {
		fmt.Println("  ✓ Chain chat  :", reply)
		if !strings.Contains(reply, "prefers email") {
			fmt.Println("    ✗ expected working-memory note echoed in reply")
			pass = false
		}
	}

	// 1b. Streaming through the same compiled runnable's Stream().
	full, n, err := ob.Stream(ctx, "acme", "alice", "s1", "Where is my package?")
	if err != nil {
		fmt.Println("  ✗ Stream error:", err)
		pass = false
	} else {
		fmt.Printf("  ✓ Streamed    : %d chunks -> %q\n", n, strings.TrimSpace(full))
		if n < 2 {
			fmt.Println("    ✗ expected multiple stream chunks")
			pass = false
		}
	}

	// 1c. ReAct agent with a real Eino tool (lookup_order).
	ans, err := ob.Agent(ctx, "What is the status of my order?")
	if err != nil {
		fmt.Println("  ✗ Agent error:", err)
		pass = false
	} else {
		fmt.Println("  ✓ ReAct agent :", ans)
		if !strings.Contains(strings.ToLower(ans), "shipped") {
			fmt.Println("    ✗ expected the tool result folded into the answer")
			pass = false
		}
	}
	return pass
}

// part2Isolation exercises feature #1 against Eino's real checkpoint state
// model. Eino is an orchestration library with no tenancy primitive; its
// compose.CheckPointStore is an interrupt/resume KV keyed by one developer
// string, so per-tenant isolation is 100% developer key-composition.
//
// Pattern (mirrors aisotropy's isolation_e2e_test.go):
//  1. Seed tenant A's secret under a checkpoint ID.
//  2. Positive control: A reads its own checkpoint ID -> secret MUST appear.
//  3. Correct path: B reads its own tenant-scoped ID -> miss (isolation holds).
//  4. Footgun path: keying by a bare session ID (no tenant) collapses A and B
//     onto one key. This is what any string-keyed KV does — the point is that
//     Eino has no tenant dimension to catch the omission, not that Eino "leaks".
//
// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
func part2Isolation(ctx context.Context) bool {
	fmt.Println("[Part 2] Isolation matrix vs Eino's real CheckPointStore")
	fmt.Println(strings.Repeat("-", 64))
	fmt.Println("  Eino state model: compose.CheckPointStore is Get/Set(ctx, checkpointID string)")
	fmt.Println("  — an interrupt/resume KV, not a memory tier. One bare string keys all state;")
	fmt.Println("  no tenant dimension exists, so scoping is the developer's key-composition.")
	fmt.Println()

	marker := "SECRET-ea-" + "9f3c1b7d2a" // distinctive tenant-A-only token
	pass := true

	// ---- Correct path: developer namespaces the checkpoint ID by tenant ----
	hCorrect, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		return false
	}
	if err := hCorrect.Seed(ctx, "tenantA:shared-session", marker); err != nil {
		fmt.Println("  ✗ seed(tenantA) failed:", err)
		return false
	}

	// Positive control: tenant A reads its own scoped checkpoint.
	got, hit, err := hCorrect.Read(ctx, "tenantA:shared-session")
	if err != nil {
		fmt.Println("  ✗ positive-control read error:", err)
		pass = false
	} else if hit && got == marker {
		fmt.Println("  ✓ Positive control: tenantA read its own secret via 'tenantA:shared-session'")
	} else {
		fmt.Printf("  ✗ Positive control FAILED (hit=%v got=%q)\n", hit, got)
		pass = false
	}

	// Correct path: tenant B uses its OWN scoped ID -> miss (isolation holds).
	got, hit, err = hCorrect.Read(ctx, "tenantB:shared-session")
	if err != nil {
		fmt.Println("  ✗ correct-path read error:", err)
		pass = false
	} else if !hit || got != marker {
		fmt.Println("  ✓ Isolated (correct path): tenantB:shared-session did NOT resolve tenantA's secret")
	} else {
		fmt.Println("  ✗ unexpected cross-read on the correct path")
		pass = false
	}

	fmt.Println()

	// ---- Footgun path: SAME checkpoint ID reused across tenants ----
	// A real, one-line-apart mistake: the dev keys the checkpoint by the raw
	// client session ID and forgets to bind the tenant, so two tenants collapse
	// onto one key. A string-keyed KV then returns whatever was stored there —
	// true of any such store, including aisotropy's BadgerDB. Eino simply has no
	// tenant dimension to notice the omission.
	// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	hCollapse, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		return false
	}
	sharedID := "shared-session" // no tenant prefix — the footgun path
	if err := hCollapse.Seed(ctx, sharedID, marker); err != nil {
		fmt.Println("  ✗ seed(shared) failed:", err)
		return false
	}
	fmt.Printf("  Tenant A seeded secret under checkpoint ID %q (no tenant prefix)\n", sharedID)

	// Tenant B composes the same bare session ID -> the keys collapse.
	got, hit, err = hCollapse.Read(ctx, sharedID)
	if err != nil {
		fmt.Println("  ✗ collapse-probe read error:", err)
		pass = false
	} else if hit && got == marker {
		fmt.Printf("  ! COLLAPSE: tenant B composes the same key %q and the store returns %s\n", sharedID, got)
		fmt.Println("     Not an Eino bug — a string-keyed KV returns what was stored under the key")
		fmt.Println("     you ask for. The failure is the dropped tenant segment; Eino has no tenant")
		fmt.Println("     dimension to catch it. aisotropy composes the key from ctx so this can't happen.")
		// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	} else {
		fmt.Printf("  (no collapse this run: hit=%v got=%q)\n", hit, got)
	}

	// Direct-probe evidence: the real store holds A's state under a bare string.
	keys := hCollapse.Store().Keys()
	fmt.Printf("  Real CheckPointStore keyspace after tenant A: %v\n", keys)
	fmt.Println("     -> a bare []string of developer-chosen IDs; no tenant/user field anywhere.")

	// The smoke PASSES iff the product-blind key collapsed onto the seed as
	// classified (feature #1 = NOT-POSSIBLE by framework). A non-collapse would
	// mean the harness itself is broken.
	collapsed := hit && got == marker
	if !collapsed {
		fmt.Println("  ✗ expected the shared-ID keys to collapse; they did not")
		pass = false
	}
	return pass
}
