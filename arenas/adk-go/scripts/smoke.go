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
	fmt.Println("Isolation model: composite (appName, userID, sessionID) key — 3 native")
	fmt.Println("slots, appName bound at runner construction. This deployment spends")
	fmt.Println("appName on the app, so tenant rides in userID and is the residual axis:")
	fmt.Println("keep it and the key isolates; drop it and two tenants collide. A")
	fmt.Println("deployment that spends appName on tenant makes tenant native instead")
	fmt.Println("(then product is the residual — see cmd/productiso). Exactly one of")
	// SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — the three-slot
	// arity holds for ADK Go in v2.0.0 and v2.1.0, and no first-party Go extension adds a
	// fourth axis. Memory Bank's N-key scope map would remove the fold, but ADK Go's binding
	// does not expose it.
	fmt.Println("{tenant, product} lacks a native slot; the developer chooses which.")
	fmt.Println(strings.Repeat("-", 62))

	if rep.Failed {
		fmt.Println("\nResult: FAILED — a correct-key probe leaked or the positive control broke.")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS — real ADK Go end-to-end works; the composite key isolates on")
	fmt.Println("the correct path; and dropping the residual axis (tenant, in userID) is a")
	fmt.Println("genuine same-session read (LEAK* rows above) — a chosen-mapping residual,")
	fmt.Println("not an ADK inability to isolate.")
}
