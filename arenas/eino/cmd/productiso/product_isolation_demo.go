// product_isolation_demo — GAP 1: the THIRD isolation axis (product_id).
//
// aisotropy scopes every memory read/write by the tuple
// (tenant_id, user_id, session_id, product_id). The other arenas only ever
// exercised tenant/user. This demo adds product_id and shows — by RUNNING it
// against the REAL CloudWeGo Eino v0.9.12 checkpoint runtime — exactly what
// Eino does and does NOT give you on that axis.
//
// The honest framing (matches FEATURES.md):
//   - Eino is a graph-ORCHESTRATION library (LangGraph-for-Go). It has NO
//     tenancy/memory-isolation primitive — same as LangGraph or Pydantic AI.
//   - compose.CheckPointStore is `Get/Set(ctx, id string)`: a human-in-the-loop
//     interrupt/resume KV, NOT a memory tier. product_id is not a dimension the
//     framework knows about — only a substring the developer concatenates.
//   - Per-scope isolation is therefore 100% developer key-composition. It is
//     CORRECT if you include the segments (Scenario A) and COLLAPSES two products
//     onto one key if you drop a segment (Scenario B). Both are shown, at equal
//     weight. This is a DIY classification (product = CONVENTION-ONLY), NOT an
//     Eino bug.
//   - What Scenario B demonstrates is true of EVERY string-keyed KV, including
//     aisotropy's BadgerDB: a store faithfully returns whatever was written under
//     the key you query. No string-keyed store can catch a wrong composed key.
//     aisotropy's edge is not "it can't leak" — it is that it composes the scope
//     key from context.Context by construction, so a developer CANNOT pass a
//     short/wrong key in the first place (unforgeable Scope wired by default).
//
// Everything runs OFFLINE (no API key; Eino's real checkpoint store).
//
// Run:  go run ./cmd/productiso     (from arenas/eino/)
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"eino-arena/orbit"
)

// key composers — both produce the single bare string that Eino's
// compose.CheckPointStore actually keys on. The framework sees only the string.

// fullKey is the CORRECT composition: every isolation axis is in the key.
func fullKey(tenant, user, product, session string) string {
	return tenant + ":" + user + ":" + product + ":" + session
}

// productBlindKey is the footgun: the developer forgot the product segment. It
// is a realistic one-line mistake — product_id is a convention aisotropy binds
// by construction (from ctx), but here it is just a string the dev must remember
// to concat. Nothing catches the omission.
func productBlindKey(tenant, user, session string) string {
	return tenant + ":" + user + ":" + session
}

func main() {
	ctx := context.Background()
	line := strings.Repeat("=", 68)
	fmt.Println(line)
	fmt.Println("Eino — GAP 1: multi-PRODUCT isolation (3rd axis) on the REAL runtime")
	fmt.Println(line)
	fmt.Println("aisotropy scopes memory by (tenant, user, session, PRODUCT).")
	// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	fmt.Println("Eino is a graph-orchestration library with no tenancy primitive.")
	fmt.Println("compose.CheckPointStore keys on ONE developer string (an interrupt/resume")
	fmt.Println("KV, not a memory tier) — product_id is not a dimension the framework knows")
	fmt.Println("about, only a substring the developer builds. So per-product isolation is")
	fmt.Println("100% developer key-composition: correct if you include product (A), collapsed")
	fmt.Println("if you drop it (B). Both paths run below, at equal weight.")
	fmt.Println()

	const (
		tenant  = "acme"
		user    = "alice"
		prodX   = "productX"
		prodY   = "productY"
		session = "s-42"
	)
	marker := "SECRET-productX-" + "7b41e9a0c2" // productX-only token

	pass := true

	// ---- Scenario A: CORRECT composition (product IS in the key) ----------
	fmt.Println(strings.Repeat("-", 68))
	fmt.Println("[A] Correct path: product_id is part of the composed checkpoint key")
	fmt.Println("    -> isolation HOLDS. This is the supported, correct usage.")
	fmt.Println(strings.Repeat("-", 68))

	hOK, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		os.Exit(1)
	}

	kX := fullKey(tenant, user, prodX, session)
	if err := hOK.Seed(ctx, kX, marker); err != nil {
		fmt.Println("  ✗ seed(productX) failed:", err)
		os.Exit(1)
	}
	fmt.Printf("  productX seeded marker under key %q\n", kX)

	// (a) Positive control: productX reads its OWN key.
	got, hit, err := hOK.Read(ctx, kX)
	switch {
	case err != nil:
		fmt.Println("  ✗ positive-control read error:", err)
		pass = false
	case hit && got == marker:
		fmt.Printf("  ✓ Positive control: productX read its own marker via %q\n", kX)
	default:
		fmt.Printf("  ✗ Positive control FAILED (hit=%v got=%q)\n", hit, got)
		pass = false
	}

	// (b) productY uses its OWN fully qualified key -> MISS (isolation holds).
	kY := fullKey(tenant, user, prodY, session)
	got, hit, err = hOK.Read(ctx, kY)
	switch {
	case err != nil:
		fmt.Println("  ✗ negative-probe read error:", err)
		pass = false
	case !hit || got != marker:
		fmt.Printf("  ✓ Isolated: productY key %q does NOT resolve productX's marker\n", kY)
	default:
		fmt.Println("  ✗ unexpected cross-read on the correct path")
		pass = false
	}
	fmt.Println("  NOTE: isolation holds because the two products get two different keys.")
	fmt.Println("        Eino ran no product check — it compared two distinct strings, which is")
	fmt.Println("        exactly the right behavior for a correctly composed key.")

	fmt.Println()

	// ---- Scenario B: FOOTGUN (product dropped -> two products collapse) ----
	fmt.Println(strings.Repeat("-", 68))
	fmt.Println("[B] Footgun path: dev's key composer forgot the product segment")
	fmt.Println("    -> productX and productY COLLAPSE onto one key. DIY classification, not a bug.")
	fmt.Println(strings.Repeat("-", 68))

	hCollapse, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		os.Exit(1)
	}

	// productX writes with the product-BLIND key (the omission).
	blind := productBlindKey(tenant, user, session)
	if err := hCollapse.Seed(ctx, blind, marker); err != nil {
		fmt.Println("  ✗ seed(product-blind) failed:", err)
		os.Exit(1)
	}
	fmt.Printf("  productX (tenant=%s user=%s) seeded marker under product-BLIND key %q\n", tenant, user, blind)
	fmt.Printf("  With product dropped, %s's key composes to the SAME string %q...\n", prodY, blind)

	// productY, composing a product-blind key, lands on the identical string.
	got, hit, err = hCollapse.Read(ctx, blind)
	collapsed := false
	switch {
	case err != nil:
		fmt.Println("  ✗ collapse-probe read error:", err)
		pass = false
	case hit && got == marker:
		collapsed = true
		fmt.Printf("  ! COLLAPSE: productY composes the same key %q and the store returns the\n", blind)
		fmt.Printf("    value written there: %s\n", got)
		fmt.Println("    This is NOT Eino returning the wrong thing — a string-keyed KV faithfully")
		fmt.Println("    returns what was stored under the key you asked for. EVERY string KV does")
		fmt.Println("    this, including aisotropy's BadgerDB. The failure is the DROPPED SEGMENT,")
		fmt.Println("    which the framework has no way to notice: product_id was never a dimension.")
	default:
		fmt.Printf("  (no collapse this run: hit=%v got=%q)\n", hit, got)
	}

	// Direct-probe evidence: the real store's keyspace has no product field.
	keys := hCollapse.Store().Keys()
	fmt.Printf("  Real CheckPointStore keyspace: %v\n", keys)
	fmt.Println("     -> a bare []string. There is no product_id column, tag, or filter anywhere")
	fmt.Println("        in Eino's Get/Set(ctx, id string) interface — product isolation is 100%")
	fmt.Println("        the developer's key-composition, exactly like tenant/user (GAP already shown).")

	// The proof PASSES iff the product-blind read actually collapsed onto the
	// seeded value (product = CONVENTION-ONLY). A non-collapse would mean the
	// demo harness itself is broken.
	if !collapsed {
		fmt.Println("  ✗ expected the product-blind key to collapse onto the seed; it did not")
		pass = false
	}

	fmt.Println()
	fmt.Println(line)
	if pass {
		fmt.Println("PRODUCT-ISOLATION RESULT: PASS — real Eino exercised; both paths reproduced.")
		fmt.Println("Verdict: product_id is CONVENTION-ONLY (a developer substring). Correct keying")
		fmt.Println("isolates (A); a dropped segment collapses two products onto one key (B). Eino")
		fmt.Println("binds nothing to product_id, so it cannot catch the omission — no string-keyed")
		fmt.Println("store can (incl. aisotropy's BadgerDB). aisotropy's edge is upstream: it composes")
		fmt.Println("the scope key from context.Context, so a wrong/short key is unrepresentable.")
		return
	}
	fmt.Println("PRODUCT-ISOLATION RESULT: FAIL — see markers above.")
	os.Exit(1)
}
