package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/aisotropy"
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

// discardLogger returns a *slog.Logger writing to io.Discard, so run() tests
// exercise every logging branch without polluting test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// withFakeRunApp swaps the runApp seam (the only piece of run() that would
// otherwise bind a real network listener) for the duration of a test, and
// restores the real aisotropy.App.Run implementation on cleanup.
func withFakeRunApp(t *testing.T, fake func(ctx context.Context, app *aisotropy.App) error) {
	t.Helper()
	prev := runApp
	runApp = fake
	t.Cleanup(func() { runApp = prev })
}

// rsaPublicKeyPEM PEM-encodes pub in the PKIX form ParseRSAPublicKeyPEM
// expects, mirroring aisotropy/authenv_test.go's helper.
func rsaPublicKeyPEM(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// TestRun_RefusalPropagatesAsError proves run() never calls os.Exit itself: an
// auth boot refusal (no authentication configured) surfaces as a returned
// error, leaving the exit-code decision entirely to main().
func TestRun_RefusalPropagatesAsError(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("AISOTROPY_ADDR", "")

	err := run(context.Background(), discardLogger())
	if err == nil {
		t.Fatal("expected an error when no authentication is configured")
	}
	if !strings.Contains(err.Error(), "aisotropyd refusing to start") {
		t.Errorf("error = %q, want it to mention the refusal", err.Error())
	}
}

// TestRun_RunAppErrorIsWrapped proves a runApp (HTTP server) failure is
// wrapped and returned rather than the process exiting directly, and that the
// built *aisotropy.App is passed through to the seam.
func TestRun_RunAppErrorIsWrapped(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")
	t.Setenv("AISOTROPY_ADDR", "127.0.0.1:0")

	wantErr := errors.New("bind failed: address in use")
	withFakeRunApp(t, func(_ context.Context, app *aisotropy.App) error {
		if app == nil {
			t.Error("expected a non-nil *aisotropy.App to be passed to runApp")
		}
		return wantErr
	})

	err := run(context.Background(), discardLogger())
	if err == nil {
		t.Fatal("expected the runApp error to propagate")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %q, want it to wrap %q", err.Error(), wantErr.Error())
	}
	if !strings.Contains(err.Error(), "aisotropyd exited with error") {
		t.Errorf("error = %q, want the exited-with-error prefix", err.Error())
	}
}

// TestRun_SuccessAllAuthModesAndOpsPostures drives run() through every auth
// mode branch (hs256, rs256 static key, rs256 jwks, noauth) crossed with the
// ops-token-set/unset and TLS-enforcement-on/off logging branches, using the
// runApp fake so no test in this suite ever binds a real network listener.
func TestRun_SuccessAllAuthModesAndOpsPostures(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name: "hs256",
			setup: func(t *testing.T) {
				t.Setenv("AISOTROPY_JWT_SECRET", validSecret)
			},
		},
		{
			name: "rs256-static",
			setup: func(t *testing.T) {
				t.Setenv("AISOTROPY_JWT_PUBLIC_KEY", rsaPublicKeyPEM(t, &rsaKey.PublicKey))
			},
		},
		{
			name: "rs256-jwks",
			setup: func(t *testing.T) {
				t.Setenv("AISOTROPY_JWT_JWKS_URL", "https://issuer.example.com/.well-known/jwks.json")
			},
		},
		{
			name: "noauth-loopback",
			setup: func(t *testing.T) {
				t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")
				t.Setenv("AISOTROPY_ADDR", "127.0.0.1:0")
			},
		},
	}

	for _, opsTok := range []string{"", "s3cr3t-ops-token"} {
		for _, tlsVal := range []string{"", "true"} {
			for _, tc := range cases {
				t.Run(tc.name+"/ops="+opsTok+"/tls="+tlsVal, func(t *testing.T) {
					clearAuthEnv(t)
					t.Setenv("AISOTROPY_ADDR", "")
					tc.setup(t)
					t.Setenv("AISOTROPY_OPS_TOKEN", opsTok)
					t.Setenv("AISOTROPY_REQUIRE_TLS", tlsVal)

					var gotCtx context.Context
					withFakeRunApp(t, func(ctx context.Context, app *aisotropy.App) error {
						gotCtx = ctx
						if app == nil {
							t.Error("expected a non-nil *aisotropy.App")
						}
						return nil
					})

					ctx := context.Background()
					if err := run(ctx, discardLogger()); err != nil {
						t.Fatalf("run() error = %v, want nil", err)
					}
					if gotCtx != ctx { //nolint:govet // pointer/interface identity check is intentional here
						t.Error("expected the same ctx to be forwarded to runApp")
					}
				})
			}
		}
	}
}

// cleanEnv builds a child-process environment from the current process's
// environment with every AISOTROPY_*/ANTHROPIC_*/RQLITE_*/QDRANT_* variable
// stripped, then layers extra on top. This keeps the TestMain_ExitCodes
// subprocess cases deterministic regardless of what the developer's shell (or
// CI) happens to have set ambiently.
func cleanEnv(extra ...string) []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(key, "AISOTROPY_"),
			strings.HasPrefix(key, "ANTHROPIC_"),
			strings.HasPrefix(key, "RQLITE_"),
			strings.HasPrefix(key, "QDRANT_"):
			continue
		}
		filtered = append(filtered, kv)
	}
	return append(filtered, extra...)
}

// TestMain_ExitCodes drives the real main() — the 4-line os.Exit wrapper
// around run() — in a re-exec'd subprocess. main()'s failure path calls
// os.Exit(1) directly, which cannot be exercised in-process without killing
// the test binary, so this follows the standard Go subprocess pattern (cf.
// os/exec_test.go's TestHelperProcess): the test re-executes its own test
// binary with an env sentinel that makes it call main() instead of running
// the normal test suite, then asserts on the child's exit code.
func TestMain_ExitCodes(t *testing.T) {
	if os.Getenv("AISOTROPYD_TEST_BE_MAIN") == "1" {
		// Re-exec'd child: act as the compiled binary's entrypoint. The success
		// case fakes runApp so main() never binds a real network listener.
		if os.Getenv("AISOTROPYD_TEST_FAKE_RUNAPP") == "1" {
			runApp = func(context.Context, *aisotropy.App) error { return nil }
		}
		main()
		return
	}

	cases := []struct {
		name     string
		env      []string
		wantCode int
	}{
		{
			name: "success_exits_0",
			env: []string{
				"AISOTROPY_ALLOW_NOAUTH=1",
				"AISOTROPY_ADDR=127.0.0.1:0",
				"AISOTROPYD_TEST_FAKE_RUNAPP=1",
			},
			wantCode: 0,
		},
		{
			name:     "refusal_exits_1",
			env:      nil, // no auth configured, no NoAuth opt-in -> resolveAuthBoot refuses
			wantCode: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ExitCodes$")
			cmd.Env = cleanEnv(append([]string{"AISOTROPYD_TEST_BE_MAIN=1"}, tc.env...)...)
			out, err := cmd.CombinedOutput()

			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else {
					t.Fatalf("unexpected exec error: %v, output: %s", err, out)
				}
			}
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d; output: %s", code, tc.wantCode, out)
			}
		})
	}
}
