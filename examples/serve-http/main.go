// Command serve-http is the LESSON 09 worked example: the SMALLEST possible
// Aisotropy HTTP server. Where the library-first examples call App.Turn
// in-process, this one serves the same runtime over HTTP with App.Run — the
// deployment shape — so a client can stream a chat over SSE.
//
// It is deliberately minimal: no tools, no persona, no knowledge base. Just the
// keyless stub LLM and an EXPLICIT NoAuth() opt-out so every /v1 route works
// with no credential (without NoAuth the runtime is default-deny and returns 401
// on every /v1 request — failing closed is the deliberate default). Run it, then
// curl the streaming endpoint; see README.md for the exact commands and a note
// on deploying the memory tiers with the repo's docker-compose.
//
// It is a SEPARATE Go module (examples/serve-http/go.mod, replace => ../..)
// importing ONLY github.com/Luum-Home/aisotropy/aisotropy, so Go's
// internal-package rule enforces "public surface only".
//
// Run it keyless (no API key, no Docker) — see README.md:
//
//	go run .
package main

import (
	"context"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	app := aisotropy.New(
		// HTTP listen address. Default is AISOTROPY_ADDR or ":8080"; set explicitly
		// here so the README's curl commands are unambiguous.
		aisotropy.WithAddr(":8080"),

		// EXPLICITLY run without a credential check so this example is usable out of
		// the box: the URL path tenant/user are trusted verbatim. A real deployment
		// swaps NoAuth() for a JWT authenticator (see the auth-scopes example).
		aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	)

	// Run boots the full runtime (router -> Huma API -> handlers, tiered memory,
	// quota, circuit breaker, cost meter) and blocks until ctx is cancelled,
	// then shuts down gracefully. With no ANTHROPIC_API_KEY the credential-less
	// stub answers, so this serves with zero configuration.
	log.Println("serve-http listening on :8080 (keyless stub LLM, NoAuth)")
	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("serve-http exited with error: %v", err)
	}
}
