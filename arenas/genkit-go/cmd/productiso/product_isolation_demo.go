// product_isolation_demo — RUN-VERIFIED probe of the THIRD isolation axis
// (product_id) against the REAL Firebase Genkit Go framework
// (github.com/firebase/genkit/go v1.10.0), fully OFFLINE via the arena's
// registered fake model. No API key, no network.
//
// Run:  go run ./cmd/productiso      (from arenas/genkit-go)
//
// WHY THIS EXISTS
// ---------------
// aisotropy scopes every memory read/write by FOUR keys travelling in
// context.Context: (tenant_id, user_id, session_id, product_id). The earlier
// isolation smoke (scripts/smoke.go) only stressed the tenant/user axes.
// product_id — the axis that separates two products of the SAME tenant+user
// (e.g. alice@acme's "orbit-crm" chat vs her "orbit-helpdesk" chat) — was
// never tested. This demo closes that gap.
//
// THE POINT
// ---------
// Genkit-Go v1.10.0 DOES ship an experimental session-memory primitive (ai/exp
// SessionStore + localstore.NewInMemorySessionStore), but it is keyed by a
// developer-chosen session id with NO tenant/user/product binding — its
// SessionSnapshot/SessionState carry only a SessionID field, so it gives you
// PERSISTENCE, not ISOLATION. There is no product (or tenant) primitive to bind
// at all. Multi-tenancy — and multi-PRODUCT-cy — is therefore entirely
// developer-passed: you build the store key yourself. product_id is just
// another string the developer may or may not fold into that key. The framework
// binds it to NOTHING. So:
//
//	(a) POSITIVE CONTROL — productX reads its own marker back. Works.
//	(b) CROSS-PRODUCT LEAK — same tenant+user, a request for a DIFFERENT
//	    product reads productX's marker, because the product axis never
//	    entered the developer's key. The real genkit.Generate pipeline hands
//	    the fake model productX's history and it faithfully reveals the
//	    secret. That is the leak.
//
// HONESTY CONTRACT: this is a LEAK, and it is labelled a leak. Genkit enforces
// nothing on the product axis — there is no product primitive to enforce with.
// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (L0 on the
// data-plane rung: no framework-owned scope of any axis).
// The only thing standing between products is developer discipline, and the
// demo shows exactly what happens the moment that discipline slips.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"genkit-go-arena/orbit"
)

func marker(tag string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "SECRET:orbit-" + tag + "-" + hex.EncodeToString(b)
}

// keyTenantUserSession models the VERY COMMON developer choice: scope the
// conversation store by tenant:user:session. Note the product axis is absent —
// nothing in Genkit reminds the developer it should be here, because Genkit has
// no concept of a product (or of any scope at all).
func keyTenantUserSession(tenant, user, session string) string {
	return tenant + ":" + user + ":" + session
}

// keyWithProduct models the developer who DID remember to include the product.
// This isolates products — but only by convention. Genkit still validates
// nothing; mis-passing the product string re-opens the leak (shown below).
func keyWithProduct(tenant, user, session, product string) string {
	return tenant + ":" + user + ":" + session + ":" + product
}

func main() {
	fmt.Println(strings.Repeat("=", 68))
	fmt.Println("Genkit Go — THIRD-axis isolation probe: product_id (offline, real framework)")
	fmt.Println(strings.Repeat("=", 68))

	ctx := context.Background()
	app, err := orbit.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit init failed:", err)
		os.Exit(2)
	}

	// Same tenant, same user — two products. This is the axis aisotropy's
	// (tenant,user,session,PRODUCT) scope separates and the earlier smoke never
	// touched.
	const tenant, user, session = "acme", "alice", "s1"
	const productX = "orbit-crm"      // where the secret lives
	const productY = "orbit-helpdesk" // a DIFFERENT product of the same user

	secretX := marker("crm")
	fmt.Printf("\nSetup: tenant=%s user=%s — two products: %q (holds the secret) and %q\n",
		tenant, user, productX, productY)
	fmt.Println("       aisotropy would scope memory by (tenant,user,session,PRODUCT), so")
	fmt.Println("       alice's CRM history can never surface in her Helpdesk product.")

	ok := true

	// ---------------------------------------------------------------
	// SCENARIO 1 — developer scopes key by tenant:user:session (product
	// OMITTED). This is the realistic default: Genkit gives no product hook, so
	// product simply never makes it into the key.
	// ---------------------------------------------------------------
	fmt.Println("\n" + strings.Repeat("-", 68))
	fmt.Println("[scenario 1] Store key = tenant:user:session  (product axis absent)")
	fmt.Println(strings.Repeat("-", 68))

	sharedKey := keyTenantUserSession(tenant, user, session)
	fmt.Printf("  developer key for BOTH products -> %q\n", sharedKey)

	// Seed: alice's CRM product stores a secret. product_id=productX is passed
	// on the request but the developer's key ignores it.
	if _, err := app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: sharedKey,
		Prompt: "Please remember my " + productX + " account recovery code: " + secretX,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "seed failed:", err)
		os.Exit(2)
	}
	fmt.Printf("  seeded %s marker under %s: %s\n", productX, productX, secretX)

	// (a) POSITIVE CONTROL — productX reads its own marker through the real
	// genkit.Generate pipeline.
	posOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: sharedKey,
		Prompt: "What is my " + productX + " account recovery code?",
	})
	if strings.Contains(posOut.Reply, secretX) {
		fmt.Printf("  ✓ POSITIVE CONTROL: %s read its own marker back through genkit.Generate\n", productX)
	} else {
		fmt.Printf("  ✗ POSITIVE CONTROL FAILED — %s could not read its own marker; got %q\n", productX, posOut.Reply)
		ok = false
	}

	// (b) CROSS-PRODUCT LEAK — same tenant+user, product_id=productY (WRONG
	// product), same session. Because product never entered the key, this reads
	// productX's history and the real model reveals productX's secret.
	leakOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: sharedKey,
		Prompt: "What is my " + productY + " account recovery code?",
	})
	if strings.Contains(leakOut.Reply, secretX) {
		fmt.Printf("  ✗ CROSS-PRODUCT LEAK: a %s request (same tenant+user) read %s's secret\n", productY, productX)
		fmt.Printf("      real model reply -> %q\n", trunc(leakOut.Reply))
		fmt.Printf("      root cause: product_id is a developer-passed field the framework binds to NOTHING;\n")
		fmt.Printf("                  it never entered the memory key, so %s and %s share one context.\n", productX, productY)
	} else {
		fmt.Printf("  ~ no leak observed in scenario 1 (unexpected for this framework); got %q\n", trunc(leakOut.Reply))
		ok = false
	}

	// ---------------------------------------------------------------
	// SCENARIO 2 — developer DID fold product into the key. Products isolate...
	// but purely by convention, and mis-passing the product string re-leaks.
	// ---------------------------------------------------------------
	fmt.Println("\n" + strings.Repeat("-", 68))
	fmt.Println("[scenario 2] Store key = tenant:user:session:product  (product folded in)")
	fmt.Println(strings.Repeat("-", 68))

	secretX2 := marker("crm2")
	keyX := keyWithProduct(tenant, user, session, productX)
	keyY := keyWithProduct(tenant, user, session, productY)

	app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: keyX,
		Prompt: "Please remember my " + productX + " account recovery code: " + secretX2,
	})
	fmt.Printf("  seeded %s marker under %q: %s\n", productX, keyX, secretX2)

	// Correctly-namespaced productY request -> different key -> isolated.
	isoOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: keyY,
		Prompt: "What is my " + productY + " account recovery code?",
	})
	if strings.Contains(isoOut.Reply, secretX2) {
		fmt.Printf("  ✗ leak even with product in key (unexpected)\n")
		ok = false
	} else {
		fmt.Printf("  ✓ isolated: %s request with its OWN key did NOT read %s's secret\n", productY, productX)
		fmt.Printf("      BUT this is convention, not enforcement — nothing forced product into the key.\n")
	}

	// The convention leak: developer accidentally reuses productX's key for a
	// productY request (typo, stale variable, copy-paste). Genkit validates
	// nothing, so the leak returns.
	misOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: tenant, UserID: user, SessionKey: keyX, // WRONG: productX's key on a productY request
		Prompt: "What is my " + productY + " account recovery code?",
	})
	if strings.Contains(misOut.Reply, secretX2) {
		fmt.Printf("  ✗ CONVENTION LEAK: %s request + %s's key -> real model returned %s's secret\n", productY, productX, productX)
		fmt.Printf("      Genkit never checks that the product in the key matches the request's product_id.\n")
	} else {
		fmt.Printf("  ~ convention leak not reproduced; got %q\n", trunc(misOut.Reply))
		ok = false
	}

	// ---------------------------------------------------------------
	fmt.Println("\n" + strings.Repeat("=", 68))
	fmt.Println("vs. aisotropy: product_id rides in context.Context alongside tenant/user/session")
	fmt.Println("   and is bound into the memory Scope by construction. A cross-product read is")
	fmt.Println("   unrepresentable (Scope{} has unexported fields) — not a discipline the")
	// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	fmt.Println("   developer must remember. Genkit has no product primitive to enforce at all.")
	fmt.Println(strings.Repeat("=", 68))

	if ok {
		fmt.Println("\nResult: PASS — positive control verified AND the cross-product leak reproduced")
		fmt.Println("Verdict: product isolation is CONVENTION (arguably weaker: no primitive at all).")
		os.Exit(0)
	}
	fmt.Println("\nResult: FAIL — expected leak/positive-control behaviour did not reproduce; see above")
	os.Exit(1)
}

func trunc(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 84 {
		return s[:84] + "…"
	}
	return s
}
