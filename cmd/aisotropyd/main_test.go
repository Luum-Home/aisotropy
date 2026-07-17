package main

import (
	"strings"
	"testing"
)

// clearAuthEnv forces every authentication-selecting env var to empty for the
// duration of a test, so a resolveAuthBoot case is not perturbed by an ambient
// value in the developer's shell. t.Setenv restores the prior value on cleanup.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AISOTROPY_JWT_SECRET",
		"AISOTROPY_JWT_PUBLIC_KEY",
		"AISOTROPY_JWT_PUBLIC_KEY_FILE",
		"AISOTROPY_JWT_JWKS_URL",
		"AISOTROPY_JWT_MAX_TTL",
		"AISOTROPY_ALLOW_NOAUTH",
		"AISOTROPY_ALLOW_NOAUTH_NONLOCAL",
	} {
		t.Setenv(k, "")
	}
}

// validSecret is a >= 32-byte HS256 signing secret (33 bytes).
const validSecret = "this-is-a-32-byte-minimum-secret!"

// TestResolveAuthBoot_NoJWTNoOptInRefuses is acceptance criterion (a): with no JWT
// configured AND no AISOTROPY_ALLOW_NOAUTH opt-in, the boot decision is a refusal —
// the binary must not come up wide open by omission.
func TestResolveAuthBoot_NoJWTNoOptInRefuses(t *testing.T) {
	clearAuthEnv(t)

	dec := resolveAuthBoot(":8080")
	if dec.refuse == "" {
		t.Fatal("expected a boot refusal when no auth is configured and NoAuth is not opted in")
	}
	if dec.authr != nil {
		t.Error("expected a nil Authenticator on refusal")
	}
	// The refusal must name the escape hatches so the operator can act.
	if !strings.Contains(dec.refuse, "AISOTROPY_JWT_SECRET") || !strings.Contains(dec.refuse, "AISOTROPY_ALLOW_NOAUTH") {
		t.Errorf("refusal message should name the config options; got: %q", dec.refuse)
	}
}

// TestResolveAuthBoot_NoAuthLoopbackBoots is acceptance criterion (b): an explicit
// AISOTROPY_ALLOW_NOAUTH=1 on a loopback bind boots cleanly (preserving local-dev
// keyless ergonomics that examples/ and dev flows rely on).
func TestResolveAuthBoot_NoAuthLoopbackBoots(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")

	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		t.Run(addr, func(t *testing.T) {
			dec := resolveAuthBoot(addr)
			if dec.refuse != "" {
				t.Fatalf("expected a clean boot on loopback NoAuth, got refusal: %q", dec.refuse)
			}
			if dec.mode != "noauth" {
				t.Errorf("mode = %q, want noauth", dec.mode)
			}
			if dec.authr == nil {
				t.Error("expected a non-nil NoAuth Authenticator")
			}
		})
	}
}

// TestResolveAuthBoot_NoAuthNonLocalRefuses proves the second gate at the boot layer:
// even with AISOTROPY_ALLOW_NOAUTH=1, a non-loopback bind refuses unless
// AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 is ALSO set.
func TestResolveAuthBoot_NoAuthNonLocalRefuses(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")

	// Default AISOTROPY_ADDR (":8080") is a wildcard bind — not loopback.
	dec := resolveAuthBoot(":8080")
	if dec.refuse == "" {
		t.Fatal("expected a refusal for NoAuth on a non-loopback (:8080 wildcard) bind")
	}
	if !strings.Contains(dec.refuse, "AISOTROPY_ALLOW_NOAUTH_NONLOCAL") {
		t.Errorf("refusal should point at the non-local override; got: %q", dec.refuse)
	}

	t.Setenv("AISOTROPY_ALLOW_NOAUTH_NONLOCAL", "1")
	dec = resolveAuthBoot(":8080")
	if dec.refuse != "" {
		t.Fatalf("expected boot with the explicit non-local override, got refusal: %q", dec.refuse)
	}
	if dec.mode != "noauth" {
		t.Errorf("mode = %q, want noauth", dec.mode)
	}
}

// TestResolveAuthBoot_JWTIgnoresAllowNoAuth is acceptance criterion (c): a configured
// JWT secret yields the real authenticator; AISOTROPY_ALLOW_NOAUTH is ignored and the
// non-local guard does not apply (jwt mode is not noauth), so a wildcard bind is fine.
func TestResolveAuthBoot_JWTIgnoresAllowNoAuth(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("AISOTROPY_JWT_SECRET", validSecret)
	t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")

	dec := resolveAuthBoot(":8080")
	if dec.refuse != "" {
		t.Fatalf("expected a clean boot with a configured JWT, got refusal: %q", dec.refuse)
	}
	if dec.mode != "jwt-hs256" {
		t.Errorf("mode = %q, want jwt-hs256 (ALLOW_NOAUTH must be ignored)", dec.mode)
	}
	if dec.authr == nil {
		t.Error("expected a live authenticator")
	}
}

// TestResolveAuthBoot_WeakSecretRefusesWithoutLeak proves a sub-minimum HS256 secret
// is a fatal config fault AND that the refusal message does not echo the secret value.
func TestResolveAuthBoot_WeakSecretRefusesWithoutLeak(t *testing.T) {
	clearAuthEnv(t)
	const weak = "short-secret-value"
	t.Setenv("AISOTROPY_JWT_SECRET", weak)

	dec := resolveAuthBoot("127.0.0.1:8080")
	if dec.refuse == "" {
		t.Fatal("expected a refusal for a sub-minimum HS256 secret")
	}
	if strings.Contains(dec.refuse, weak) {
		t.Errorf("refusal message leaked the secret value: %q", dec.refuse)
	}
}
