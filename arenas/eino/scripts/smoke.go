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
		fmt.Println("SMOKE RESULT: PASS — real Eino stack exercised; isolation proof produced expected leak.")
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

// part2Isolation proves feature #1 against Eino's real checkpoint state model.
//
// Pattern (mirrors aisotropy's isolation_e2e_test.go):
//  1. Seed tenant A's secret under a checkpoint ID.
//  2. Positive control: A reads its own checkpoint ID -> secret MUST appear.
//  3. Negative probe (correct path): B reads its own tenant-scoped ID -> miss.
//  4. LEAK probe (shared ID): A and B share a session ID -> B reads A's secret.
func part2Isolation(ctx context.Context) bool {
	fmt.Println("[Part 2] Adversarial isolation vs Eino's real CheckPointStore")
	fmt.Println(strings.Repeat("-", 64))
	fmt.Println("  Eino state model: compose.CheckPointStore is Get/Set(ctx, checkpointID string).")
	fmt.Println("  One bare string keys all persisted state. No tenant dimension exists.")
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

	// Negative probe (correct path): tenant B uses its OWN scoped ID -> miss.
	got, hit, err = hCorrect.Read(ctx, "tenantB:shared-session")
	if err != nil {
		fmt.Println("  ✗ negative-probe read error:", err)
		pass = false
	} else if !hit || got != marker {
		fmt.Println("  ✓ No leak (correct path): tenantB:shared-session did not resolve tenantA's secret")
	} else {
		fmt.Println("  ✗ unexpected leak on the correct path")
		pass = false
	}

	fmt.Println()

	// ---- Leak path: SAME checkpoint ID reused across tenants ----
	// A real, one-line-apart mistake: the dev keys the checkpoint by the raw
	// client session ID and forgets to bind the tenant. Eino cannot stop this.
	hLeak, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		return false
	}
	sharedID := "shared-session" // no tenant prefix — the leaky path
	if err := hLeak.Seed(ctx, sharedID, marker); err != nil {
		fmt.Println("  ✗ seed(shared) failed:", err)
		return false
	}
	fmt.Printf("  Tenant A seeded secret under checkpoint ID %q (no tenant prefix)\n", sharedID)

	// Tenant B presents the same session ID. Eino resumes A's checkpoint.
	got, hit, err = hLeak.Read(ctx, sharedID)
	if err != nil {
		fmt.Println("  ✗ leak-probe read error:", err)
		pass = false
	} else if hit && got == marker {
		fmt.Printf("  ✗ LEAK CONFIRMED: tenant B resumed %q and Eino returned tenant A's secret: %s\n", sharedID, got)
		fmt.Println("     Driven by Eino's real runtime — the checkpoint ID carries no tenant.")
	} else {
		fmt.Printf("  (no leak this run: hit=%v got=%q)\n", hit, got)
	}

	// Direct-probe evidence: the real store holds A's state under a bare string.
	keys := hLeak.Store().Keys()
	fmt.Printf("  Real CheckPointStore keyspace after tenant A: %v\n", keys)
	fmt.Println("     -> a bare []string of developer-chosen IDs; no tenant/user field anywhere.")

	// The isolation proof PASSES the smoke iff the framework leaked as expected
	// (feature #1 is NOT covered by Eino). A non-leak would mean our proof is
	// broken, so treat the confirmed leak as the expected outcome.
	leaked := hit && got == marker
	if !leaked {
		fmt.Println("  ✗ expected the shared-ID leak to reproduce; it did not")
		pass = false
	}
	return pass
}
