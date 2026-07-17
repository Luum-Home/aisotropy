package aisotropy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/auth"
)

// ErrNoAuthenticator is returned by AuthenticatorFromEnv when NO JWT authenticator
// is configured (no HS256 secret, no RS256 static key, no JWKS URL) AND NoAuth was
// not EXPLICITLY opted into via AISOTROPY_ALLOW_NOAUTH. It exists to close the
// zero-config-insecure hole: historically the mere ABSENCE of a JWT secret silently
// produced NoAuth() (path tenant_id/user_id trusted with no credential), so a
// deployment that simply forgot to configure auth would come up wide open. Now the
// insecure posture is never reachable by omission — the caller (the aisotropyd boot
// path) turns this sentinel into a fatal, non-zero exit. It is a server-side
// configuration outcome, never a per-request authentication result.
var ErrNoAuthenticator = errors.New("aisotropy: no authenticator configured — set a JWT " +
	"secret/key (AISOTROPY_JWT_SECRET, AISOTROPY_JWT_PUBLIC_KEY[_FILE], or AISOTROPY_JWT_JWKS_URL), " +
	"or explicitly set AISOTROPY_ALLOW_NOAUTH=1 to run WITHOUT authentication (local dev only)")

// ErrNoAuthNonLocal is returned by NoAuthNonLocalGuard when NoAuth is active (an
// explicit AISOTROPY_ALLOW_NOAUTH=1 opt-in) but the runtime is bound to a
// NON-loopback address, and the operator has not ALSO set the second, deliberately
// separate AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 override. Exposing an unauthenticated
// runtime on a routable interface is a cross-tenant disclosure by construction (any
// caller sets tenant_id in the URL and reads that tenant's data), so it must be much
// harder to reach than a local-dev keyless boot.
var ErrNoAuthNonLocal = errors.New("aisotropy: NoAuth (no credential verification) requested " +
	"on a non-loopback bind address — bind AISOTROPY_ADDR to loopback (127.0.0.1/localhost/::1), " +
	"configure a JWT authenticator, or set AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 to override (STRONGLY discouraged)")

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
//   - none set -> NoAuth is NOT the silent default. If (and only if)
//     AISOTROPY_ALLOW_NOAUTH is explicitly truthy (1/true) this returns NoAuth()
//     — the insecure opt-out that trusts the URL path tenant/user verbatim, mode
//     label "noauth". Otherwise it returns ErrNoAuthenticator so the boot path
//     REFUSES TO START rather than come up wide open by omission. The additional
//     non-loopback guard (NoAuthNonLocalGuard) makes NoAuth on a routable
//     interface harder still.
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
		// No JWT authenticator configured. NoAuth is reachable ONLY as an explicit,
		// affirmative opt-in — never by mere omission. Without AISOTROPY_ALLOW_NOAUTH
		// the boot path receives ErrNoAuthenticator and refuses to start.
		if envTruthy("AISOTROPY_ALLOW_NOAUTH") {
			return NoAuth(), "noauth", nil
		}
		return nil, "noauth", ErrNoAuthenticator
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

// envTruthy reports whether the named env var is set to an affirmative boolean
// (1/t/T/TRUE/true/True, per strconv.ParseBool). An unset, empty, or unparseable
// value is false — so an insecure opt-in is never triggered by accident (e.g. a
// typo like "yes" does NOT enable NoAuth).
func envTruthy(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// NoAuthNonLocalGuard is the second, deliberately-separate gate on running WITHOUT
// authentication. It is called by the boot path ONLY when NoAuth is already the
// selected mode (an explicit AISOTROPY_ALLOW_NOAUTH=1 opt-in). It returns nil when
// the runtime binds a loopback address (safe for local dev) OR when the operator
// has explicitly set AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1; otherwise it returns
// ErrNoAuthNonLocal so the binary refuses to expose an unauthenticated runtime on a
// routable interface. addr is the resolved AISOTROPY_ADDR listen address (e.g.
// ":8080", "127.0.0.1:8080", "0.0.0.0:8080", "[::1]:8080").
func NoAuthNonLocalGuard(addr string) error {
	if isLoopbackListenAddr(addr) {
		return nil
	}
	if envTruthy("AISOTROPY_ALLOW_NOAUTH_NONLOCAL") {
		return nil
	}
	return ErrNoAuthNonLocal
}

// isLoopbackListenAddr reports whether a host:port (or bare host) listen address
// binds a loopback interface. A wildcard bind — an empty host (":8080") or
// 0.0.0.0/:: — is NOT loopback: it exposes the port on every interface, so NoAuth
// there must clear the non-local guard. "localhost" is treated as loopback.
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		// No port component; treat the whole value as the host.
		host = strings.TrimSpace(addr)
	}
	switch strings.ToLower(host) {
	case "":
		return false // wildcard bind (":8080") — all interfaces
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A non-loopback hostname (or anything unresolvable) is treated as
		// non-local: fail safe toward requiring the explicit override.
		return false
	}
	return ip.IsLoopback()
}
