// Command auth-scopes is the LESSON 04 worked example: authentication and
// scope-gated tools at the HTTP boundary. It is a keyless, runnable server that
// makes two invariants concrete:
//
//   - AUTHENTICATION is a network-boundary concern. In-process App.Turn trusts
//     the caller's supplied identity (your own code is inside the trust
//     boundary); the credential check exists only on the HTTP surface App.Run
//     serves, where identity arrives from an untrusted client. So this example
//     drives the HTTP path.
//   - SCOPES gate tools, and when a real authenticator vouched for them the
//     VERIFIED scopes are authoritative: a client cannot smuggle extra scopes in
//     the request body to unlock a tool. A tool whose RequiredScope the caller
//     lacks is never offered to the model and never runs.
//
// It boots with a JWT authenticator (NewJWTAuthenticator) over a demo HS256
// secret, registers one scope-gated typed tool (reveal_secret, RequiredScope
// "read:secrets"), then mints two demo tokens on startup — one WITH the scope,
// one WITHOUT — and prints ready-to-run curl commands for each. Hit the server
// with each token to see the tool offered vs withheld.
//
// It is a SEPARATE Go module (examples/auth-scopes/go.mod, replace => ../..)
// that imports ONLY github.com/Luum-Home/aisotropy/aisotropy — Go's
// internal-package rule physically forbids reaching the private packages, so the
// fact that it compiles is the "public surface only" guarantee.
//
// Run it keyless (no API key, no Docker) — see README.md:
//
//	go run .
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// demoSecret is the HS256 signing secret for this example's JWT gate. It MUST be
// at least 32 bytes (NewJWTAuthenticator enforces a minimum key length). This is
// a demo constant printed alongside the tokens it signs; a real deployment reads
// the secret from AISOTROPY_JWT_SECRET (or an external IdP via RS256/JWKS) and
// never hard-codes or logs it.
var demoSecret = []byte("auth-scopes-demo-hs256-secret-32b!")

// revealSecretArgs is the reveal_secret tool's typed argument contract. The
// json/jsonschema tags drive both the schema the model is shown and the decode
// of the model's arguments.
type revealSecretArgs struct {
	Vault string `json:"vault" jsonschema:"description=name of the vault to read"`
}

// revealSecret is the scope-gated tool handler. It only ever runs for a caller
// whose VERIFIED scopes include read:secrets — the runtime never offers the tool
// to the model otherwise, so reaching this function already implies the caller
// was authorized. It reads the calling tenant from ctx (read-only) to stay
// tenant-aware without touching an internal memory scope.
func revealSecret(ctx context.Context, args revealSecretArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	return fmt.Sprintf("secret for tenant %q in vault %q: hunter2", tenant, args.Vault), nil
}

func main() {
	app := aisotropy.New(
		aisotropy.WithAddr(":8080"),

		// A JWT bearer gate over the demo HS256 secret. The runtime verifies the
		// token on every /v1 request and binds it MUST-AGREE to the URL path
		// tenant/user (mismatch -> 403; missing/invalid -> 401). The token's
		// scope/scopes claim becomes the caller's VERIFIED, authoritative scopes.
		aisotropy.WithAuthenticator(aisotropy.NewJWTAuthenticator(demoSecret)),

		// One scope-gated TYPED tool: the model is offered reveal_secret only when
		// the caller's verified scopes cover read:secrets.
		aisotropy.WithTypedTool("reveal_secret", "reveal a stored secret for the tenant", "read:secrets", revealSecret),

		aisotropy.WithPersona("vault-bot", aisotropy.Persona{
			BasePrompt: "You are VaultBot. Only reveal secrets when explicitly asked and permitted.",
		}),
	)

	// Mint two demo tokens for tenant "acme", user "alice": one carrying the
	// read:secrets scope, one with none. Both bind to the /tenant/acme/user/alice
	// path; only the first unlocks reveal_secret.
	exp := time.Now().Add(time.Hour).Unix()
	withScope := mintHS256(demoSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"scope": "read:secrets", "exp": exp,
	})
	noScope := mintHS256(demoSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": exp,
	})

	fmt.Println("auth-scopes demo server starting on :8080 (keyless stub LLM)")
	fmt.Println()
	fmt.Println("# (A) token WITH read:secrets — reveal_secret is OFFERED to the model:")
	fmt.Printf("curl -s localhost:8080/v1/tenant/acme/user/alice/chat \\\n"+
		"  -H 'authorization: Bearer %s' \\\n"+
		"  -H 'content-type: application/json' \\\n"+
		"  -d '{\"prompt\":\"reveal the prod secret\",\"product_id\":\"vault-bot\",\"session_id\":\"s1\"}'\n\n", withScope)
	fmt.Println("# (B) token WITHOUT the scope — reveal_secret is WITHHELD (same request):")
	fmt.Printf("curl -s localhost:8080/v1/tenant/acme/user/alice/chat \\\n"+
		"  -H 'authorization: Bearer %s' \\\n"+
		"  -H 'content-type: application/json' \\\n"+
		"  -d '{\"prompt\":\"reveal the prod secret\",\"product_id\":\"vault-bot\",\"session_id\":\"s1\"}'\n\n", noScope)
	fmt.Println("# (C) no token at all -> 401; (D) token (A) against /tenant/other/... -> 403 (must-agree bind).")
	fmt.Println()

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("auth-scopes exited with error: %v", err)
	}
}

// mintHS256 builds a minimal HS256 JWT (header.payload.signature, base64url, no
// padding) using only the standard library — mirroring the stdlib-only verifier
// NewJWTAuthenticator installs (the same pattern as the package's godoc
// ExampleWithAuthenticator).
func mintHS256(secret []byte, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
