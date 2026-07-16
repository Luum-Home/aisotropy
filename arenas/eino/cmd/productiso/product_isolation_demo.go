// product_isolation_demo — GAP 1: the THIRD isolation axis (product_id).
//
// aisotropy scopes every memory read/write by the tuple
// (tenant_id, user_id, session_id, product_id). The other arenas only ever
// exercised tenant/user. This demo adds product_id and proves — by RUNNING it
// against the REAL CloudWeGo Eino v0.9.12 checkpoint runtime — that product_id
// is NOTHING but another developer-composed key segment. The framework never
// binds it to anything, so a wrong product_id reads another product's private
// state. Everything runs OFFLINE (no API key; Eino's real checkpoint store).
//
// Structure mirrors aisotropy's isolation proof but on the product axis:
//  1. Positive control: tenantA/userA/productX seeds a marker under a fully
//     qualified key and reads its OWN marker back.
//  2. Correct path: tenantA/userA/productY, using its OWN fully qualified key,
//     MISSES productX's marker (isolation holds ONLY because the dev remembered
//     to put product in the key).
//  3. CROSS-PRODUCT LEAK: a one-line-apart mistake — the dev's key composer
//     drops the product segment (product_id is "just an id", easy to forget).
//     Now tenantA/userA/productY reads the exact same product-less key and Eino
//     hands back productX's secret. The framework enforces nothing on the extra
//     key because, to Eino, it was never a dimension — only a substring.
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

// productBlindKey is the BUG: the developer forgot the product segment. It is a
// realistic, one-line mistake — product_id is a convention aisotropy binds by
// construction, but here it is just a string the dev must remember to concat.
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
	fmt.Println("Eino's compose.CheckPointStore keys on ONE developer string — product_id")
	fmt.Println("is not a dimension the framework knows about, only a substring the dev builds.")
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
		fmt.Printf("  ✓ No leak (correct path): productY key %q did not resolve productX's marker\n", kY)
	default:
		fmt.Println("  ✗ unexpected leak on the correct path")
		pass = false
	}
	fmt.Println("  NOTE: isolation held ONLY because the dev remembered to put product in the key.")
	fmt.Println("        Eino ran no product check — it compared two different strings, nothing more.")

	fmt.Println()

	// ---- Scenario B: CROSS-PRODUCT LEAK (product dropped from the key) -----
	fmt.Println(strings.Repeat("-", 68))
	fmt.Println("[B] CROSS-PRODUCT LEAK: dev's key composer forgot the product segment")
	fmt.Println(strings.Repeat("-", 68))

	hLeak, err := orbit.NewIsolationHarness(ctx)
	if err != nil {
		fmt.Println("  ✗ harness build failed:", err)
		os.Exit(1)
	}

	// productX writes with the product-BLIND key (the bug).
	blind := productBlindKey(tenant, user, session)
	if err := hLeak.Seed(ctx, blind, marker); err != nil {
		fmt.Println("  ✗ seed(product-blind) failed:", err)
		os.Exit(1)
	}
	fmt.Printf("  productX (tenant=%s user=%s) seeded marker under product-BLIND key %q\n", tenant, user, blind)
	fmt.Printf("  Same tenant+user, DIFFERENT product (%s) now issues a read...\n", prodY)

	// productY presents the SAME product-blind key. Eino resumes productX's state.
	got, hit, err = hLeak.Read(ctx, blind)
	leaked := false
	switch {
	case err != nil:
		fmt.Println("  ✗ leak-probe read error:", err)
		pass = false
	case hit && got == marker:
		leaked = true
		fmt.Printf("  ✗ LEAK CONFIRMED: productY resumed key %q and Eino returned productX's secret:\n", blind)
		fmt.Printf("       %s\n", got)
		fmt.Println("     product_id was never bound by the framework — dropping it from the string")
		fmt.Println("     silently merged two products' memory. Eino cannot stop this by construction.")
	default:
		fmt.Printf("  (no leak this run: hit=%v got=%q)\n", hit, got)
	}

	// Direct-probe evidence: the real store's keyspace has no product field.
	keys := hLeak.Store().Keys()
	fmt.Printf("  Real CheckPointStore keyspace: %v\n", keys)
	fmt.Println("     -> a bare []string. There is no product_id column, tag, or filter anywhere")
	fmt.Println("        in Eino's Get/Set(ctx, id string) interface — product isolation is 100%")
	fmt.Println("        the developer's string-hygiene, exactly like tenant/user (GAP already shown).")

	// The proof PASSES iff the wrong-product read actually leaked (feature NOT
	// covered by Eino). A non-leak would mean the demo is broken.
	if !leaked {
		fmt.Println("  ✗ expected the cross-product leak to reproduce; it did not")
		pass = false
	}

	fmt.Println()
	fmt.Println(line)
	if pass {
		fmt.Println("PRODUCT-ISOLATION RESULT: PASS — real Eino exercised; cross-product leak reproduced.")
		fmt.Println("Verdict: product_id is CONVENTION-ONLY (a developer substring); the framework")
		fmt.Println("binds nothing to it, so a wrong product_id reads another product's state.")
		return
	}
	fmt.Println("PRODUCT-ISOLATION RESULT: FAIL — see markers above.")
	os.Exit(1)
}
