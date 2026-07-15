package aisotropy

import (
	"fmt"
	"os"
	"time"

	"github.com/Luum-Home/aisotropy/internal/auth"
)

// AuthenticatorFromEnv builds the Authenticator the aisotropyd binary should
// install, based on the environment — the same FromEnv idiom used by
// internal/llm.FromEnv. It makes the shipped binary SECURABLE BY CONFIG: an
// operator can enable authentication without forking the code or embedding the
// library, just by setting an env var.
//
// Precedence:
//   - AISOTROPY_JWT_SECRET set (non-empty) -> NewJWTAuthenticator([]byte(secret)),
//     verifying an "Authorization: Bearer <jwt>" HS256 credential (stdlib only,
//     no dependency). Mode label "jwt-hs256". The secret is validated FAIL-FAST
//     against auth.ValidateHS256Secret: a secret shorter than the HS256 minimum
//     (32 bytes) returns a non-nil error so the binary REFUSES TO BOOT on a
//     brute-forceable key rather than silently degrading to a fail-closed
//     verifier that rejects every request.
//   - otherwise -> NoAuth(), the explicit insecure opt-out that trusts the URL
//     path tenant/user verbatim. Mode label "noauth".
//
// Optional hardening applied when the JWT authenticator is selected (each unset
// = not checked, so existing tokens keep verifying):
//   - AISOTROPY_JWT_AUDIENCE — require the token "aud" claim to contain it.
//   - AISOTROPY_JWT_ISSUER   — require the token "iss" claim to equal it.
//   - AISOTROPY_JWT_MAX_TTL  — cap the token lifetime (a Go duration, e.g. "1h");
//     a malformed value is a configuration fault and returns an error.
//
// The second return is a short mode label for logging ONLY — the secret value
// itself is never returned, logged, or otherwise exposed. The third return is a
// server-side configuration error (weak secret, malformed max-TTL) the boot
// path turns into a fatal exit; it is never a per-request authentication
// outcome. RS256/JWKS is a documented follow-on and deliberately not wired here
// to avoid adding a dependency.
func AuthenticatorFromEnv() (Authenticator, string, error) {
	secret := os.Getenv("AISOTROPY_JWT_SECRET")
	if secret == "" {
		return NoAuth(), "noauth", nil
	}

	// Fail-fast on a weak signing secret: refuse to boot rather than install a
	// verifier that would reject every request against a brute-forceable key.
	if err := auth.ValidateHS256Secret([]byte(secret)); err != nil {
		return nil, "jwt-hs256", err
	}

	var opts []JWTOption
	if aud := os.Getenv("AISOTROPY_JWT_AUDIENCE"); aud != "" {
		opts = append(opts, WithJWTAudience(aud))
	}
	if iss := os.Getenv("AISOTROPY_JWT_ISSUER"); iss != "" {
		opts = append(opts, WithJWTIssuer(iss))
	}
	if ttl := os.Getenv("AISOTROPY_JWT_MAX_TTL"); ttl != "" {
		d, perr := time.ParseDuration(ttl)
		if perr != nil {
			return nil, "jwt-hs256", fmt.Errorf("AISOTROPY_JWT_MAX_TTL is not a valid Go duration: %w", perr)
		}
		opts = append(opts, WithJWTMaxTTL(d))
	}

	return NewJWTAuthenticator([]byte(secret), opts...), "jwt-hs256", nil
}
