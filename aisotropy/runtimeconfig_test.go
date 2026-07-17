package aisotropy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateEnv snapshots the given env vars, unsets them for the duration of the
// test, and restores their original presence/value on cleanup. It keeps the
// env-mutating config tests hermetic and independent of the ambient environment
// (and of each other), so the suite is safe to run sequentially under -race.
func isolateEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		orig, had := os.LookupEnv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			} else {
				_ = os.Unsetenv(k)
			}
		})
		_ = os.Unsetenv(k)
	}
}

func ptrBool(b bool) *bool        { return &b }
func ptrFloat(f float64) *float64 { return &f }

const sampleRuntimeYAML = `
addr: ":9191"
require_tls: true
data_dir: "/var/lib/aisotropy"
dedicated_tenants: ["acme", "globex"]
rate_per_sec: 25
burst: 55
cost_budget_usd: 12.5
fact_min_importance: 0.4
guardrails_block_output: true
qdrant_host: "qdrant.local"
qdrant_port: 6334
qdrant_use_tls: false
jwt_secret: "file-secret"
`

const sampleRuntimeTOML = `
addr = ":9292"
require_tls = true
data_dir = "/srv/aisotropy"
dedicated_tenants = ["acme", "globex"]
rate_per_sec = 25.0
burst = 55
cost_budget_usd = 12.5
fact_min_importance = 0.4
guardrails_block_output = true
qdrant_host = "qdrant.local"
qdrant_port = 6334
qdrant_use_tls = false
jwt_secret = "file-secret"
`

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestLoadRuntimeConfigFile_YAMLAndTOML verifies both parsers produce the same
// typed config from equivalent files — the file format is a surface detail, the
// mirrored env-knob schema is identical.
func TestLoadRuntimeConfigFile_YAMLAndTOML(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
	}{
		{"yaml", "aisotropy.runtime.yaml", sampleRuntimeYAML},
		{"toml", "aisotropy.runtime.toml", sampleRuntimeTOML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadRuntimeConfigFile(writeTemp(t, tc.file, tc.body))
			if err != nil {
				t.Fatalf("LoadRuntimeConfigFile: %v", err)
			}
			if c.RequireTLS == nil || !*c.RequireTLS {
				t.Errorf("RequireTLS = %v, want true", c.RequireTLS)
			}
			if c.RatePerSec == nil || *c.RatePerSec != 25 {
				t.Errorf("RatePerSec = %v, want 25", c.RatePerSec)
			}
			if c.Burst == nil || *c.Burst != 55 {
				t.Errorf("Burst = %v, want 55", c.Burst)
			}
			if c.QdrantPort == nil || *c.QdrantPort != 6334 {
				t.Errorf("QdrantPort = %v, want 6334", c.QdrantPort)
			}
			// A false pointer must be non-nil (set on purpose), distinct from absent.
			if c.QdrantUseTLS == nil || *c.QdrantUseTLS {
				t.Errorf("QdrantUseTLS = %v, want non-nil false", c.QdrantUseTLS)
			}
			if len(c.DedicatedTenants) != 2 || c.DedicatedTenants[0] != "acme" {
				t.Errorf("DedicatedTenants = %v, want [acme globex]", c.DedicatedTenants)
			}
			if c.JWTSecret != "file-secret" {
				t.Errorf("JWTSecret = %q, want file-secret", c.JWTSecret)
			}
		})
	}
}

// TestMaterialize_FileOverridesDefault proves the file value reaches the process
// env (and hence a downstream resolver's default) when no explicit env var is
// present: env absent -> file wins over the built-in default.
func TestMaterialize_FileOverridesDefault(t *testing.T) {
	isolateEnv(t, "AISOTROPY_RATE_PER_SEC", "AISOTROPY_ADDR")

	// Baseline: with neither env nor file, the resolver returns the built-in default.
	if got := ratePerSecFromEnv(); got != defaultRatePerSec {
		t.Fatalf("baseline ratePerSecFromEnv() = %v, want default %v", got, defaultRatePerSec)
	}

	c := RuntimeConfig{Addr: ":9191", RatePerSec: ptrFloat(25)}
	applied, err := c.materialize()
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got := os.Getenv("AISOTROPY_ADDR"); got != ":9191" {
		t.Errorf("AISOTROPY_ADDR = %q, want :9191 (file fills the unset env)", got)
	}
	if got := ratePerSecFromEnv(); got != 25 {
		t.Errorf("ratePerSecFromEnv() = %v, want 25 (file overrides default)", got)
	}
	if len(applied) != 2 {
		t.Errorf("applied = %v, want 2 keys", applied)
	}
}

// TestMaterialize_EnvOverridesFile proves an explicit env var wins over the file:
// materialize leaves an already-present variable untouched.
func TestMaterialize_EnvOverridesFile(t *testing.T) {
	isolateEnv(t, "AISOTROPY_RATE_PER_SEC", "AISOTROPY_ADDR")
	t.Setenv("AISOTROPY_RATE_PER_SEC", "99")
	t.Setenv("AISOTROPY_ADDR", ":7000")

	c := RuntimeConfig{Addr: ":9191", RatePerSec: ptrFloat(25)}
	applied, err := c.materialize()
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got := os.Getenv("AISOTROPY_ADDR"); got != ":7000" {
		t.Errorf("AISOTROPY_ADDR = %q, want :7000 (env must win over file)", got)
	}
	if got := ratePerSecFromEnv(); got != 99 {
		t.Errorf("ratePerSecFromEnv() = %v, want 99 (env must win over file)", got)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %v, want 0 keys (both already set in env)", applied)
	}
}

// TestMaterialize_EnvOverridesFile_EmptyEnvStillWins proves precedence is by
// presence, not truthiness: an explicitly-empty env var still beats the file.
func TestMaterialize_EnvOverridesFile_EmptyEnvStillWins(t *testing.T) {
	isolateEnv(t, "AISOTROPY_DATA_DIR")
	t.Setenv("AISOTROPY_DATA_DIR", "")

	c := RuntimeConfig{DataDir: "/from/file"}
	if _, err := c.materialize(); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got := os.Getenv("AISOTROPY_DATA_DIR"); got != "" {
		t.Errorf("AISOTROPY_DATA_DIR = %q, want empty (explicit empty env wins over file)", got)
	}
}

// TestMaterialize_BoolFormatting checks bool knobs serialize to strconv.ParseBool
// / == "true" compatible strings so the downstream env resolvers read them.
func TestMaterialize_BoolFormatting(t *testing.T) {
	isolateEnv(t, "AISOTROPY_REQUIRE_TLS", "QDRANT_USE_TLS", "AISOTROPY_GUARDRAILS_BLOCK_OUTPUT")

	c := RuntimeConfig{
		RequireTLS:            ptrBool(true),
		QdrantUseTLS:          ptrBool(true),
		GuardrailsBlockOutput: ptrBool(true),
	}
	if _, err := c.materialize(); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !requireTLSFromEnv() {
		t.Error("requireTLSFromEnv() = false, want true from file")
	}
	if os.Getenv("QDRANT_USE_TLS") != "true" {
		t.Errorf("QDRANT_USE_TLS = %q, want \"true\" (== \"true\" comparison downstream)", os.Getenv("QDRANT_USE_TLS"))
	}
	if !guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT") {
		t.Error("guardrailsBlockFromEnv = false, want true from file")
	}
}

// TestLoadAndMaterialize_MissingFileIsNoOp verifies that with no config file
// configured (no env var, no default file on disk) loading is a clean no-op —
// this is the 100%-backward-compatible path.
func TestLoadAndMaterialize_MissingFileIsNoOp(t *testing.T) {
	isolateEnv(t, configFileEnv)
	// The package test cwd contains no aisotropy.runtime.* default files, and the
	// config-file env var is unset, so resolution finds nothing to load.
	applied, err := LoadAndMaterializeRuntimeConfig("")
	if err != nil {
		t.Fatalf("LoadAndMaterializeRuntimeConfig = %v, want nil (clean no-op)", err)
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil (nothing materialized)", applied)
	}
}

// TestLoadAndMaterialize_ExplicitMissingPathErrors verifies an explicitly-named
// but non-existent file is a clear error, not a silent no-op — a typo'd path
// should fail loudly rather than boot with surprising defaults.
func TestLoadAndMaterialize_ExplicitMissingPathErrors(t *testing.T) {
	_, err := LoadAndMaterializeRuntimeConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("LoadAndMaterializeRuntimeConfig(missing) = nil, want a read error")
	}
}

// TestLoadRuntimeConfigFile_MalformedErrors verifies malformed files return a
// clear error (never a panic), for both YAML and TOML, and for a value whose
// type does not match the schema.
func TestLoadRuntimeConfigFile_MalformedErrors(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
	}{
		{"broken-yaml", "bad.yaml", "addr: [1, 2\nbroken: : :"},
		{"broken-toml", "bad.toml", "addr = \nthis is not = = valid"},
		{"yaml-type-mismatch", "typ.yaml", "rate_per_sec: \"not-a-number\""},
		{"toml-type-mismatch", "typ.toml", "burst = \"not-an-int\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRuntimeConfigFile(writeTemp(t, tc.file, tc.body))
			if err == nil {
				t.Fatalf("LoadRuntimeConfigFile(%s) = nil, want a parse error", tc.name)
			}
			if !strings.Contains(err.Error(), "aisotropy:") {
				t.Errorf("error %q, want the wrapped aisotropy: prefix", err)
			}
		})
	}
}

// TestApp_WithConfigFile_HonorsFileAndEnv exercises the wired App seam end to
// end: WithConfigFile materializes the file at boot (env > file > default), and
// applyConfigFile is idempotent across repeated startRuntime calls.
func TestApp_WithConfigFile_HonorsFileAndEnv(t *testing.T) {
	isolateEnv(t, "AISOTROPY_ADDR", "AISOTROPY_RATE_PER_SEC")
	t.Setenv("AISOTROPY_RATE_PER_SEC", "77") // explicit env must win over the file

	path := writeTemp(t, "aisotropy.runtime.yaml", "addr: \":9393\"\nrate_per_sec: 25\n")
	app := New(WithConfigFile(path))

	if err := app.applyConfigFile(); err != nil {
		t.Fatalf("applyConfigFile: %v", err)
	}
	// File filled the unset AISOTROPY_ADDR...
	if got := os.Getenv("AISOTROPY_ADDR"); got != ":9393" {
		t.Errorf("AISOTROPY_ADDR = %q, want :9393 (from file)", got)
	}
	// ...but the explicit env rate wins over the file value.
	if got := ratePerSecFromEnv(); got != 77 {
		t.Errorf("ratePerSecFromEnv() = %v, want 77 (env wins over file)", got)
	}
	// Idempotent: a second application does not error and does not change state.
	if err := app.applyConfigFile(); err != nil {
		t.Fatalf("applyConfigFile (2nd) = %v, want nil (idempotent)", err)
	}
}

// TestApp_WithConfigFile_MalformedFailsBoot verifies a malformed config file
// surfaces as a clear error through the App seam rather than a panic.
func TestApp_WithConfigFile_MalformedFailsBoot(t *testing.T) {
	path := writeTemp(t, "aisotropy.runtime.yaml", "addr: [broken\n : : :")
	app := New(WithConfigFile(path))
	if err := app.applyConfigFile(); err == nil {
		t.Fatal("applyConfigFile(malformed) = nil, want a clear error")
	}
}
