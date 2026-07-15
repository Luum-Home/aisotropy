package aisotropy

import "os"

// AuthenticatorFromEnv builds the Authenticator the aisotropyd binary should
// install, based on the environment — the same FromEnv idiom used by
// internal/llm.FromEnv. It makes the shipped binary SECURABLE BY CONFIG: an
// operator can enable authentication without forking the code or embedding the
// library, just by setting an env var.
//
// Precedence:
//   - AISOTROPY_JWT_SECRET set (non-empty) -> NewJWTAuthenticator([]byte(secret)),
//     verifying an "Authorization: Bearer <jwt>" HS256 credential (stdlib only,
//     no dependency). Mode label "jwt-hs256".
//   - otherwise -> NoAuth(), the explicit insecure opt-out that trusts the URL
//     path tenant/user verbatim. Mode label "noauth".
//
// The second return is a short mode label for logging ONLY — the secret value
// itself is never returned, logged, or otherwise exposed. RS256/JWKS is a
// documented follow-on and deliberately not wired here to avoid adding a
// dependency.
func AuthenticatorFromEnv() (Authenticator, string) {
	if secret := os.Getenv("AISOTROPY_JWT_SECRET"); secret != "" {
		return NewJWTAuthenticator([]byte(secret)), "jwt-hs256"
	}
	return NoAuth(), "noauth"
}
