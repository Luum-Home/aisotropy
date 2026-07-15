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
// Exactly ONE signing mode may be configured; setting more than one is an
// ambiguous configuration that returns an error (the binary refuses to boot).
// The modes, in order of the env vars that select them:
//   - AISOTROPY_JWT_SECRET (non-empty) -> HS256 shared-secret verification
//     (stdlib only, no dependency). Mode label "jwt-hs256". The secret is
//     validated FAIL-FAST against auth.ValidateHS256Secret: a secret shorter
//     than the HS256 minimum (32 bytes) returns a non-nil error so the binary
//     REFUSES TO BOOT on a brute-forceable key. The 32-byte minimum applies to
//     HS256 ONLY.
//   - AISOTROPY_JWT_PUBLIC_KEY (PEM literal) or AISOTROPY_JWT_PUBLIC_KEY_FILE
//     (path to a PEM file) -> RS256 verification against a STATIC RSA public
//     key. Mode label "jwt-rs256". The two are mutually exclusive. A parse or
//     file-read failure returns an error (never containing key bytes).
//   - AISOTROPY_JWT_JWKS_URL -> RS256 verification against a JWKS endpoint
//     (fetched, TTL-cached, kid-matched; fetch failure fails closed). Mode label
//     "jwt-rs256-jwks".
//   - none set -> NoAuth(), the explicit insecure opt-out that trusts the URL
//     path tenant/user verbatim. Mode label "noauth".
//
// Optional hardening applied to ANY JWT mode (each unset = not checked, so
// existing tokens keep verifying):
//   - AISOTROPY_JWT_AUDIENCE — require the token "aud" claim to contain it.
//   - AISOTROPY_JWT_ISSUER   — require the token "iss" claim to equal it.
//   - AISOTROPY_JWT_MAX_TTL  — cap the token lifetime (a Go duration, e.g. "1h");
//     a malformed value is a configuration fault and returns an error.
//
// The second return is a short mode label for logging ONLY — no key material
// (HMAC secret, RSA key, JWKS URL) is ever returned, logged, or otherwise
// exposed. The third return is a server-side configuration error the boot path
// turns into a fatal exit; it is never a per-request authentication outcome.
func AuthenticatorFromEnv() (Authenticator, string, error) {
	secret := os.Getenv("AISOTROPY_JWT_SECRET")
	pubKeyPEM := os.Getenv("AISOTROPY_JWT_PUBLIC_KEY")
	pubKeyFile := os.Getenv("AISOTROPY_JWT_PUBLIC_KEY_FILE")
	jwksURL := os.Getenv("AISOTROPY_JWT_JWKS_URL")

	// Count the configured mode families. PUBLIC_KEY and PUBLIC_KEY_FILE are two
	// ways to name the SAME static-key mode, so they count once here (their
	// mutual exclusivity is checked inside the RS256 static branch).
	hs256 := secret != ""
	rs256Static := pubKeyPEM != "" || pubKeyFile != ""
	rs256JWKS := jwksURL != ""

	modes := 0
	for _, on := range []bool{hs256, rs256Static, rs256JWKS} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return nil, "", fmt.Errorf("ambiguous authentication configuration: set exactly one of " +
			"AISOTROPY_JWT_SECRET (HS256), AISOTROPY_JWT_PUBLIC_KEY[_FILE] (RS256 static key), " +
			"or AISOTROPY_JWT_JWKS_URL (RS256 JWKS)")
	}
	if modes == 0 {
		return NoAuth(), "noauth", nil
	}

	// Shared hardening options apply to whichever JWT mode is selected.
	opts, err := jwtHardeningOptsFromEnv()
	if err != nil {
		return nil, "", err
	}

	switch {
	case hs256:
		// Fail-fast on a weak signing secret: refuse to boot rather than install
		// a verifier that would reject every request against a brute-forceable key.
		if verr := auth.ValidateHS256Secret([]byte(secret)); verr != nil {
			return nil, "jwt-hs256", verr
		}
		return NewJWTAuthenticator([]byte(secret), opts...), "jwt-hs256", nil

	case rs256Static:
		if pubKeyPEM != "" && pubKeyFile != "" {
			return nil, "jwt-rs256", fmt.Errorf("ambiguous RS256 static key: set only one of " +
				"AISOTROPY_JWT_PUBLIC_KEY or AISOTROPY_JWT_PUBLIC_KEY_FILE")
		}
		pemBytes := []byte(pubKeyPEM)
		if pubKeyFile != "" {
			b, rerr := os.ReadFile(pubKeyFile)
			if rerr != nil {
				// rerr names the path/OS cause, not key contents.
				return nil, "jwt-rs256", fmt.Errorf("reading AISOTROPY_JWT_PUBLIC_KEY_FILE: %w", rerr)
			}
			pemBytes = b
		}
		pub, perr := ParseRSAPublicKeyPEM(pemBytes)
		if perr != nil {
			// ParseRSAPublicKeyPEM errors never contain key bytes.
			return nil, "jwt-rs256", fmt.Errorf("parsing RSA public key: %w", perr)
		}
		opts = append(opts, WithJWTRSAPublicKey(pub))
		return NewJWTAuthenticator(nil, opts...), "jwt-rs256", nil

	default: // rs256JWKS
		opts = append(opts, WithJWTJWKS(jwksURL))
		return NewJWTAuthenticator(nil, opts...), "jwt-rs256-jwks", nil
	}
}

// jwtHardeningOptsFromEnv builds the aud/iss/max-ttl options shared by every JWT
// mode. A malformed AISOTROPY_JWT_MAX_TTL is a configuration fault and returns
// an error.
func jwtHardeningOptsFromEnv() ([]JWTOption, error) {
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
			return nil, fmt.Errorf("AISOTROPY_JWT_MAX_TTL is not a valid Go duration: %w", perr)
		}
		opts = append(opts, WithJWTMaxTTL(d))
	}
	return opts, nil
}
