// Command productiso runs the multi-PRODUCT isolation matrix against the REAL
// Google ADK Go framework (google.golang.org/adk/v2) with an offline fake
// model. No LLM API key required.
//
//	go run ./cmd/productiso
//
// It maps the 4 axes this demo scopes by (tenant, user, session, product) onto ADK
// — note aisotropy itself binds THREE (tenant/user/session); product is a
// body-supplied persona selector here too. Corrected 2026-08-04.
// Go's 3 native key slots (appName, userID, sessionID). The STRONGEST ADK config
// gives the product axis its own native slot — one runner per product with
// appName=product, bound at runner construction and un-droppable per request —
// so a cross-product read is a storage miss by construction. The honest residual:
// with only 3 native slots for 4 axes, exactly one axis (here tenant) has no slot
// of its own, rides in userID, and is the drop-vulnerable one.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"adk-go-arena/orbit"
)

func main() {
	line := strings.Repeat("=", 62)
	fmt.Println(line)
	fmt.Println("ADK Go (google.golang.org/adk/v2) — Multi-PRODUCT Isolation Smoke")
	fmt.Println(line)
	fmt.Println("Real framework: real llmagent + real runner + real session.Service")
	fmt.Println("Model: offline FakeModel (implements model.LLM) — no API key")
	fmt.Println("aisotropy binds 3 axes (tenant, user, session) from ctx; PRODUCT is a")
	fmt.Println("body-supplied persona selector on both runtimes (corrected 2026-08-04).")
	fmt.Println("ADK has 3 native slots (appName, userID, sessionID). STRONGEST config:")
	fmt.Println("appName=product (one runner/product) makes the product axis native.")
	fmt.Println()

	rep, err := orbit.RunProductMatrix(context.Background())
	if err != nil {
		fmt.Println("harness error:", err)
		os.Exit(2)
	}

	for _, r := range rep.Results {
		mark := " ok  "
		switch {
		case r.Fatal:
			mark = "FATAL"
		case r.Leaked && r.MustIsolate:
			mark = "FAIL "
		case r.Leaked:
			mark = "LEAK*"
		}
		fmt.Printf("[%s] %s\n        -> %s\n", mark, r.Label, r.Detail)
	}
	fmt.Println("\n  ok   = isolated / control passed   LEAK* = expected leaky-path demo   FAIL = regression")

	fmt.Println()
	fmt.Println(strings.Repeat("-", 62))
	fmt.Println("Verdict: appName=product gives the product axis a NATIVE, non-droppable")
	fmt.Println("slot — a cross-product read is a construction miss, and a competent dev")
	fmt.Println("prevents the product leak this way. The honest residual: 3 native slots")
	fmt.Println("for 4 axes means exactly one axis (tenant, once appName is spent on")
	fmt.Println("product) rides in userID and is drop-vulnerable (LEAK* row). aisotropy")
	fmt.Println("binds all 4 axes from ctx, so no axis is left to convention.")
	fmt.Println(strings.Repeat("-", 62))

	if rep.Failed {
		fmt.Println("\nResult: FAILED — a correct-key probe leaked or the positive control broke.")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS — real ADK Go end-to-end works; appName=product isolates the")
	fmt.Println("product axis by construction; and the residual axis (tenant in userID),")
	fmt.Println("when dropped, is a genuine same-session read (LEAK* row above).")
}
