// Command productiso runs the multi-PRODUCT isolation matrix against the REAL
// Google ADK Go framework (google.golang.org/adk/v2) with an offline fake
// model. No LLM API key required.
//
//	go run ./cmd/productiso
//
// It proves the THIRD isolation axis aisotropy scopes on — product_id — which
// ADK Go has no primitive for. product_id is at best a fourth string the
// developer hand-folds into the (appName, userID, sessionID) key; the framework
// binds it to nothing. Fold it in and the composite key isolates; drop it (the
// natural bug — ADK has no product slot to remind you) and two products for the
// same tenant+user silently collide.
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
	fmt.Println("aisotropy scopes (tenant, user, session, PRODUCT). ADK scopes")
	fmt.Println("(appName, userID, sessionID) — product_id is a 4th axis it lacks.")
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
	fmt.Println("Verdict: product_id is [NOT-A-PRIMITIVE] in ADK Go. It isolates ONLY")
	fmt.Println("while the developer keeps folding it into the key; the framework never")
	fmt.Println("binds it to a caller, so dropping it is a one-line silent cross-product")
	fmt.Println("read (LEAK* rows). aisotropy makes product_id a first-class scope axis.")
	fmt.Println(strings.Repeat("-", 62))

	if rep.Failed {
		fmt.Println("\nResult: FAILED — a correct-key probe leaked or the positive control broke.")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS — real ADK Go end-to-end works; product-in-key isolates on the")
	fmt.Println("correct path; and dropping product_id is a proven, one-line silent")
	fmt.Println("cross-product read (LEAK* rows above).")
}
