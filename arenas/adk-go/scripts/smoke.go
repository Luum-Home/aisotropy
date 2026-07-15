// Command smoke runs the adversarial isolation matrix against the REAL Google
// ADK Go framework (google.golang.org/adk/v2) with an offline fake model.
// No LLM API key required.
//
//	go run ./scripts
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
	fmt.Println("ADK Go (google.golang.org/adk/v2) — Adversarial Isolation Smoke")
	fmt.Println(line)
	fmt.Println("Real framework: real llmagent + real runner + real session.Service")
	fmt.Println("Model: offline FakeModel (implements model.LLM) — no API key")
	fmt.Println()

	rep, err := orbit.RunMatrix(context.Background())
	if err != nil {
		fmt.Println("harness error:", err)
		os.Exit(2)
	}

	for _, r := range rep.Results {
		mark := " ok  " // isolated / positive control passed
		switch {
		case r.Fatal:
			mark = "FATAL"
		case r.Leaked && r.MustIsolate:
			mark = "FAIL " // a correct-key probe leaked — real regression
		case r.Leaked:
			mark = "LEAK*" // intentional demonstration of the leaky path
		}
		fmt.Printf("[%s] %s\n        -> %s\n", mark, r.Label, r.Detail)
	}
	fmt.Println("\n  ok   = isolated / control passed   LEAK* = expected leaky-path demo   FAIL = regression")

	fmt.Println()
	fmt.Println(strings.Repeat("-", 62))
	fmt.Println("Isolation model: developer-passed (appName,userID,sessionID) key.")
	fmt.Println("ADK Go has NO tenant primitive and does NOT bind the key to an")
	fmt.Println("authenticated caller. The composite key isolates WHEN the developer")
	fmt.Println("folds tenant into it; drop the tenant component (one line) and two")
	fmt.Println("tenants collide silently. Contrast adk-python composite-PK + #1641,")
	fmt.Println("where even the keyed backend leaked when a subquery dropped user_id.")
	fmt.Println(strings.Repeat("-", 62))

	if rep.Failed {
		fmt.Println("\nResult: FAILED — a correct-key probe leaked or the positive control broke.")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS — real ADK Go end-to-end works; composite key isolates on the")
	fmt.Println("correct path; and the leaky path (dropped tenant / unbound key) is a proven,")
	fmt.Println("one-line silent cross-tenant read (LEAK* rows above).")
}
