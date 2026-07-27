package aisotropy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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
func ptrInt(n int) *int           { return &n }

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
jwt_issuer: "https://issuer.example"
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
jwt_issuer = "https://issuer.example"
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
			// A NON-secret auth knob still loads normally: the ADR-0072 refusal
			// is scoped to secretEnvKeys, it does not quarantine the JWT family.
			if c.JWTIssuer != "https://issuer.example" {
				t.Errorf("JWTIssuer = %q, want https://issuer.example", c.JWTIssuer)
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

// TestRuntimeConfig_EnvMap_IntAndDedicatedTenants covers envMap's setInt closure
// (never exercised by the other tests, which only ever set string/float/bool
// fields) and the DedicatedTenants>0 branch, by populating Burst, QdrantPort,
// and DedicatedTenants directly and checking the materialized keys.
func TestRuntimeConfig_EnvMap_IntAndDedicatedTenants(t *testing.T) {
	c := RuntimeConfig{
		Burst:            ptrInt(55),
		QdrantPort:       ptrInt(6334),
		DedicatedTenants: []string{"acme", "globex"},
	}
	m := c.envMap()
	if m["AISOTROPY_BURST"] != "55" {
		t.Errorf("AISOTROPY_BURST = %q, want \"55\"", m["AISOTROPY_BURST"])
	}
	if m["QDRANT_PORT"] != "6334" {
		t.Errorf("QDRANT_PORT = %q, want \"6334\"", m["QDRANT_PORT"])
	}
	if m["AISOTROPY_DEDICATED_TENANTS"] != "acme,globex" {
		t.Errorf("AISOTROPY_DEDICATED_TENANTS = %q, want \"acme,globex\"", m["AISOTROPY_DEDICATED_TENANTS"])
	}
}

// TestRuntimeConfig_EnvMap_EmptyDedicatedTenantsOmitted covers the
// DedicatedTenants==0 branch (the setStr call is skipped): an empty slice must
// not materialize the key at all.
func TestRuntimeConfig_EnvMap_EmptyDedicatedTenantsOmitted(t *testing.T) {
	c := RuntimeConfig{}
	m := c.envMap()
	if _, ok := m["AISOTROPY_DEDICATED_TENANTS"]; ok {
		t.Error("AISOTROPY_DEDICATED_TENANTS must be omitted when DedicatedTenants is empty")
	}
}

// TestRuntimeConfig_Materialize_SetenvErrorSurfaces covers materialize's
// os.Setenv-error branch: a value containing a NUL byte is rejected by the OS,
// and materialize must return that error wrapped rather than silently
// continuing (or panicking).
func TestRuntimeConfig_Materialize_SetenvErrorSurfaces(t *testing.T) {
	isolateEnv(t, "AISOTROPY_ADDR")
	c := RuntimeConfig{Addr: "bad\x00value"}
	if _, err := c.materialize(); err == nil {
		t.Fatal("materialize with a NUL byte in a value must return an error, not silently set it")
	}
}

// TestLoadAndMaterialize_MaterializeErrorSurfaces covers
// LoadAndMaterializeRuntimeConfig's materialize-error propagation branch: a
// well-formed file whose value cannot be set as an env var (NUL byte) makes
// LoadAndMaterializeRuntimeConfig return that error rather than swallowing it.
func TestLoadAndMaterialize_MaterializeErrorSurfaces(t *testing.T) {
	isolateEnv(t, "AISOTROPY_ADDR")
	path := writeTemp(t, "aisotropy.runtime.yaml", "addr: \"bad\\u0000value\"\n")
	if _, err := LoadAndMaterializeRuntimeConfig(path); err == nil {
		t.Fatal("LoadAndMaterializeRuntimeConfig must surface a materialize error (NUL byte value)")
	}
}

// TestResolveConfigPath_EnvVarAndDefaultSearchPath covers resolveConfigPath's
// two remaining branches: the AISOTROPY_CONFIG_FILE env var (with no explicit
// path argument) and the default-search-paths fallback (a file present under
// one of defaultConfigSearchPaths' relative names in the current directory).
func TestResolveConfigPath_EnvVarAndDefaultSearchPath(t *testing.T) {
	isolateEnv(t, configFileEnv)

	t.Run("env var selects the path", func(t *testing.T) {
		t.Setenv(configFileEnv, "/some/configured/path.yaml")
		path, ok := resolveConfigPath("")
		if !ok || path != "/some/configured/path.yaml" {
			t.Fatalf("resolveConfigPath(\"\") = (%q,%v), want (/some/configured/path.yaml,true)", path, ok)
		}
	})

	t.Run("default search path found on disk", func(t *testing.T) {
		// resolveConfigPath probes RELATIVE paths, so run from a temp cwd.
		dir := t.TempDir()
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(cwd) })

		want := defaultConfigSearchPaths[0]
		if err := os.WriteFile(filepath.Join(dir, want), []byte("addr: \":1234\"\n"), 0o600); err != nil {
			t.Fatalf("write default search path file: %v", err)
		}

		path, ok := resolveConfigPath("")
		if !ok || path != want {
			t.Fatalf("resolveConfigPath(\"\") = (%q,%v), want (%q,true)", path, ok, want)
		}
	})
}

// TestRedactSecretKeys covers both branches: a secret-bearing key name is
// annotated "(secret)" while a non-secret key name passes through verbatim.
func TestRedactSecretKeys(t *testing.T) {
	got := redactSecretKeys([]string{"AISOTROPY_JWT_SECRET", "AISOTROPY_ADDR"})
	want := []string{"AISOTROPY_JWT_SECRET (secret)", "AISOTROPY_ADDR"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("redactSecretKeys(...) = %v, want %v", got, want)
	}
}

// TestExampleRuntimeConfig_IsRealAndInert guards the shipped reference file
// aisotropy.runtime.example.yaml against the two ways a documented example rots:
//
//  1. INVENTED KEYS — it documents a knob the code does not have (or keeps
//     documenting one that was renamed). Every commented key in the example is
//     uncommented in-memory and must round-trip through the real parser into a
//     field that materializes to a real environment variable.
//  2. NOT INERT — the file ships with every key commented out on purpose, so
//     copying it to a discovered path is a no-op until the operator opts in.
//     Parsed as-is it must produce a zero config that materializes nothing.
func TestExampleRuntimeConfig_IsRealAndInert(t *testing.T) {
	const examplePath = "../aisotropy.runtime.example.yaml"

	raw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read %s: %v — the documented example file must exist", examplePath, err)
	}

	// (2) Shipped as-is, the example must be completely inert.
	asShipped, err := LoadRuntimeConfigFile(examplePath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfigFile(example) = %v, want nil (it must parse)", err)
	}
	if applied := asShipped.envMap(); len(applied) != 0 {
		t.Errorf("the shipped example materializes %v, want nothing — every key must be commented out", applied)
	}

	// (1) Collect the keys the example documents. A documented key looks like
	// "# key: value" or "# key:" at the start of a line (after "# " only), which
	// distinguishes real knobs from the prose comments around them.
	docKey := regexp.MustCompile(`^# ([a-z][a-z0-9_]*):`)
	var documented []string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := docKey.FindStringSubmatch(strings.TrimRight(line, " \t")); m != nil {
			documented = append(documented, m[1])
		}
	}
	if len(documented) == 0 {
		t.Fatal("found no documented keys in the example — the extraction regexp is wrong")
	}

	// Every documented key must be a REAL field: uncommenting it alone must parse
	// and must materialize at least one environment variable.
	for _, key := range documented {
		t.Run(key, func(t *testing.T) {
			// A value that is valid for every field type in the schema is not
			// possible, so try the plausible ones and require that one works.
			candidates := []string{`"x"`, `1`, `1.0`, `true`, `["x"]`}
			var lastErr error
			for _, v := range candidates {
				c, err := loadYAMLString(t, key+": "+v)
				if err != nil {
					lastErr = err
					continue
				}
				if len(c.envMap()) > 0 {
					return // real key, maps to a real env var
				}
				lastErr = nil
			}
			t.Errorf("documented key %q does not map to any RuntimeConfig field "+
				"(last parse error: %v) — the example documents a knob that does not exist", key, lastErr)
		})
	}

	// And the reverse direction: every real NON-SECRET field should be documented,
	// so the example stays a complete reference as the schema grows — while every
	// SECRET field must be ABSENT as a knob (ADR-0072). The example is a file
	// operators copy; a commented-out `anthropic_api_key: "<a real key>"` line is an
	// invitation to paste a real key into a committable artifact, and it would not
	// even work now that the loader refuses it.
	secretTags := map[string]bool{}
	for _, tag := range secretYAMLKeys {
		secretTags[tag] = true
	}
	tagRe := regexp.MustCompile(`yaml:"([a-z0-9_]+)"`)
	src, err := os.ReadFile("runtimeconfig.go")
	if err != nil {
		t.Fatalf("read runtimeconfig.go: %v", err)
	}
	documentedSet := map[string]bool{}
	for _, k := range documented {
		documentedSet[k] = true
	}
	for _, m := range tagRe.FindAllStringSubmatch(string(src), -1) {
		switch {
		case secretTags[m[1]] && documentedSet[m[1]]:
			t.Errorf("secret key %q is documented as a knob in %s — secrets are refused "+
				"from config files (ADR-0072) and must not be shown as a settable key",
				m[1], examplePath)
		case !secretTags[m[1]] && !documentedSet[m[1]]:
			t.Errorf("RuntimeConfig field %q is not documented in %s", m[1], examplePath)
		}
	}

	// Belt to the braces above: no secret key may appear ANYWHERE in the example
	// in settable YAML form (`key:` at the start of a line, commented or not).
	for _, tag := range secretYAMLKeys {
		settable := regexp.MustCompile(`(?m)^\s*#?\s*` + regexp.QuoteMeta(tag) + `\s*:`)
		if settable.MatchString(string(raw)) {
			t.Errorf("%s contains a settable line for the secret key %q", examplePath, tag)
		}
	}
}

// loadYAMLString parses a one-line YAML config body through the real loader.
func loadYAMLString(t *testing.T, body string) (RuntimeConfig, error) {
	t.Helper()
	return LoadRuntimeConfigFile(writeTemp(t, "probe.yaml", body+"\n"))
}

// TestLoadRuntimeConfigFile_UnknownKeysAreIgnored PINS the deliberate decision
// on unknown keys: the loader is LENIENT — an unrecognized key is silently
// ignored and the recognized keys around it still load. This is not an accident
// of the parsers; it is the chosen behavior, and this test exists so a future
// change to strict decoding (goccy's yaml.Strict / BurntSushi's
// MetaData.Undecoded) is a deliberate, visible break rather than a silent one.
//
// Rationale for lenient over strict: RuntimeConfig MIRRORS the env surface, and
// the env surface is open — an operator's file may legitimately carry keys for a
// NEWER or OLDER aisotropy than the binary reading it (rollback, staged upgrade,
// one file shared across a fleet mid-roll). Strict decoding would turn that into
// a hard boot failure, which is a worse outcome than ignoring a knob the binary
// does not know. It also matches how the process environment itself behaves:
// an unknown AISOTROPY_* env var is ignored, never fatal. Malformed SYNTAX and
// TYPE mismatches still fail closed (TestLoadRuntimeConfigFile_MalformedErrors)
// — the leniency is about unknown NAMES, not invalid VALUES.
func TestLoadRuntimeConfigFile_UnknownKeysAreIgnored(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{
			name: "yaml",
			file: "unknown.yaml",
			body: "addr: \":9191\"\ntotally_unknown_key: 5\nanother_unknown: \"x\"\n",
			want: ":9191",
		},
		{
			name: "toml",
			file: "unknown.toml",
			body: "addr = \":9292\"\ntotally_unknown_key = 5\nanother_unknown = \"x\"\n",
			want: ":9292",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadRuntimeConfigFile(writeTemp(t, tc.file, tc.body))
			if err != nil {
				t.Fatalf("LoadRuntimeConfigFile(unknown keys) = %v, want nil (lenient)", err)
			}
			if c.Addr != tc.want {
				t.Errorf("Addr = %q, want %q (known keys must still load)", c.Addr, tc.want)
			}
		})
	}
}

// TestLoadAndMaterialize_AbsentFileMutatesNothing is the BACKWARD-COMPATIBILITY
// gate, proven rather than asserted. TestLoadAndMaterialize_MissingFileIsNoOp
// checks the return value; this checks the actual blast radius — that with no
// config file configured, the ENTIRE process environment is byte-identical
// before and after. That is the real guarantee "no file => behaves exactly as
// before this feature existed" rests on, since every downstream knob resolves
// through os.Getenv.
func TestLoadAndMaterialize_AbsentFileMutatesNothing(t *testing.T) {
	isolateEnv(t, configFileEnv)

	// Run from a temp cwd so no stray aisotropy.runtime.* in the repo (or in the
	// package dir) is discovered by the relative default search paths.
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	before := os.Environ()
	sort.Strings(before)

	applied, err := LoadAndMaterializeRuntimeConfig("")
	if err != nil {
		t.Fatalf("LoadAndMaterializeRuntimeConfig = %v, want nil", err)
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil", applied)
	}

	after := os.Environ()
	sort.Strings(after)

	if len(before) != len(after) {
		t.Fatalf("environment size changed: %d -> %d (want no mutation)", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("environment mutated at %d: %q -> %q", i, before[i], after[i])
		}
	}
}

// secretCanaries is every secret-bearing field in RuntimeConfig, keyed by the env
// var it mirrors, with a distinctive value that can be grepped for in errors and
// logs. Adding a secret field without adding it here is caught by
// TestSecretishEnvKeys_AreClassified (which derives the key list from the struct).
var secretCanaries = map[string]string{
	"ANTHROPIC_API_KEY":       "CANARY-anthropic-provider-key",
	"AISOTROPY_JWT_SECRET":    "CANARY-jwt-secret",
	"AISOTROPY_OPS_TOKEN":     "CANARY-ops-token",
	"QDRANT_API_KEY":          "CANARY-qdrant-key",
	"AISOTROPY_EMBED_API_KEY": "CANARY-embed-key",
}

// secretYAMLKeys maps each secret env var to the YAML/TOML key an operator would
// (wrongly) write in a config file.
var secretYAMLKeys = map[string]string{
	"ANTHROPIC_API_KEY":       "anthropic_api_key",
	"AISOTROPY_JWT_SECRET":    "jwt_secret",
	"AISOTROPY_OPS_TOKEN":     "ops_token",
	"QDRANT_API_KEY":          "qdrant_api_key",
	"AISOTROPY_EMBED_API_KEY": "embed_api_key",
}

func secretCanaryKeys() []string {
	keys := make([]string, 0, len(secretCanaries))
	for k := range secretCanaries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestLoadRuntimeConfigFile_RefusesSecrets is the ADR-0072 gate: a runtime config
// file that supplies ANY secret-bearing key fails to load, in BOTH formats, one
// key at a time. It asserts the four properties the decision rests on:
//
//  1. FAIL CLOSED — the load returns an error (the server does not start), not a
//     warning-and-continue.
//  2. ACTIONABLE — the message names the offending key and says to use the
//     environment instead.
//  3. NEVER ECHOES THE VALUE — the secret does not appear in the error text.
//  4. NOT MATERIALIZED — the process environment is untouched, so a refused
//     secret cannot leak into a downstream *FromEnv resolver either.
func TestLoadRuntimeConfigFile_RefusesSecrets(t *testing.T) {
	for _, envKey := range secretCanaryKeys() {
		val := secretCanaries[envKey]
		yamlKey := secretYAMLKeys[envKey]
		for _, f := range []struct{ name, file, body string }{
			{"yaml", "aisotropy.runtime.yaml", "addr: \":9191\"\n" + yamlKey + ": \"" + val + "\"\n"},
			{"toml", "aisotropy.runtime.toml", "addr = \":9191\"\n" + yamlKey + " = \"" + val + "\"\n"},
		} {
			t.Run(envKey+"/"+f.name, func(t *testing.T) {
				isolateEnv(t, envKey, "AISOTROPY_ADDR")

				c, err := LoadRuntimeConfigFile(writeTemp(t, f.file, f.body))
				if err == nil {
					t.Fatalf("LoadRuntimeConfigFile(%s in file) = nil, want a refusal", envKey)
				}
				if !errors.Is(err, ErrSecretInConfigFile) {
					t.Errorf("error %v does not wrap ErrSecretInConfigFile", err)
				}
				msg := err.Error()
				if strings.Contains(msg, val) {
					t.Errorf("SECRET LEAK: the error echoes the value: %q", msg)
				}
				if !strings.Contains(msg, envKey) {
					t.Errorf("error %q does not name the offending key %q", msg, envKey)
				}
				if !strings.Contains(msg, "environment variable") {
					t.Errorf("error %q does not tell the operator to use the environment", msg)
				}
				// The returned config must be the ZERO value: no secret survives the call.
				if len(c.envMap()) != 0 {
					t.Errorf("refused load returned a populated config %v, want the zero value", c.envMap())
				}
				// And nothing was written to the process environment.
				if got, ok := os.LookupEnv(envKey); ok {
					t.Errorf("%s was materialized (%q) despite the refusal", envKey, got)
				}
				if got, ok := os.LookupEnv("AISOTROPY_ADDR"); ok {
					t.Errorf("AISOTROPY_ADDR was materialized (%q); a refused file must apply NOTHING", got)
				}
			})
		}
	}
}

// TestLoadRuntimeConfigFile_ParseErrorNeverEchoesSecrets closes the leak path the
// refusal alone cannot reach. A MALFORMED file never gets far enough to be refused
// — it dies in the parser — and github.com/goccy/go-yaml annotates its errors with
// a SOURCE SNIPPET of the offending file. So a broken YAML file that also carries a
// real `anthropic_api_key:` line echoed that key verbatim into the boot error, and
// from there into whatever logs it. Each case below leaked before sanitizeParseError
// existed (yaml-syntax, yaml-type-elsewhere, yaml-secret-type-mismatch); the TOML
// cases did not, and are pinned anyway so a parser upgrade cannot open the hole.
func TestLoadRuntimeConfigFile_ParseErrorNeverEchoesSecrets(t *testing.T) {
	const canary = "CANARY-provider-key-must-never-appear"
	cases := []struct{ name, file, body string }{
		{"yaml-syntax-error-elsewhere", "c.yaml",
			"anthropic_api_key: \"" + canary + "\"\naddr: [1, 2\nbroken: : :\n"},
		{"yaml-type-error-elsewhere", "c.yaml",
			"anthropic_api_key: \"" + canary + "\"\nrate_per_sec: \"nan-nan\"\n"},
		{"yaml-secret-type-mismatch", "c.yaml",
			"anthropic_api_key: [\"" + canary + "\"]\n"},
		{"toml-syntax-error-elsewhere", "c.toml",
			"anthropic_api_key = \"" + canary + "\"\naddr = \nnot = = valid\n"},
		{"toml-type-error-elsewhere", "c.toml",
			"anthropic_api_key = \"" + canary + "\"\nburst = \"nope\"\n"},
		{"jwt-secret-yaml", "c.yaml",
			"jwt_secret: \"" + canary + "\"\naddr: [1, 2\nbroken: : :\n"},
		{"ops-token-yaml", "c.yaml",
			"ops_token: \"" + canary + "\"\naddr: [1, 2\nbroken: : :\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRuntimeConfigFile(writeTemp(t, tc.file, tc.body))
			if err == nil {
				t.Fatal("want a parse error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("SECRET LEAK via the parser error: %q", err.Error())
			}
			if !strings.Contains(err.Error(), "aisotropy:") {
				t.Errorf("error %q lost the wrapped aisotropy: prefix", err)
			}
		})
	}
}

// TestSecretFileKeys_MatchSecretEnvKeys pins the reflection-derived FILE-key list
// that sanitizeParseError's regexp is built from: it must name exactly the
// secret-bearing knobs, or a secret would sail through the redaction unmatched.
func TestSecretFileKeys_MatchSecretEnvKeys(t *testing.T) {
	got := append([]string(nil), secretFileKeys...)
	sort.Strings(got)
	want := make([]string, 0, len(secretYAMLKeys))
	for _, tag := range secretYAMLKeys {
		want = append(want, tag)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("secretFileKeys = %v, want %v", got, want)
	}
}

// TestSanitizeParseError_PassesThroughCleanErrors proves the sanitizer is a no-op
// when nothing needs redacting — errors.Is/As on parser errors must keep working
// for the overwhelmingly common (no-secret) case.
func TestSanitizeParseError_PassesThroughCleanErrors(t *testing.T) {
	orig := errors.New("[2:15] cannot unmarshal string into float64")
	if got := sanitizeParseError(orig); got != orig {
		t.Errorf("sanitizeParseError returned a NEW error %v for clean input; want the original", got)
	}
	if sanitizeParseError(nil) != nil {
		t.Error("sanitizeParseError(nil) must be nil")
	}
}

// TestLoadAndMaterialize_RefusesSecrets_ThroughTheAppSeam proves the refusal is
// wired all the way to boot: an App pointed at a file carrying a secret fails
// applyConfigFile (so Run/Turn return the error) instead of starting with the
// secret applied.
func TestLoadAndMaterialize_RefusesSecrets_ThroughTheAppSeam(t *testing.T) {
	isolateEnv(t, "ANTHROPIC_API_KEY", "AISOTROPY_ADDR")

	path := writeTemp(t, "aisotropy.runtime.yaml",
		"addr: \":9393\"\nanthropic_api_key: \""+secretCanaries["ANTHROPIC_API_KEY"]+"\"\n")

	if _, err := LoadAndMaterializeRuntimeConfig(path); err == nil {
		t.Fatal("LoadAndMaterializeRuntimeConfig(file with a secret) = nil, want a refusal")
	} else if !errors.Is(err, ErrSecretInConfigFile) {
		t.Errorf("error %v does not wrap ErrSecretInConfigFile", err)
	}

	app := New(WithConfigFile(path))
	err := app.applyConfigFile()
	if err == nil {
		t.Fatal("applyConfigFile(file with a secret) = nil, want the boot to fail closed")
	}
	if strings.Contains(err.Error(), secretCanaries["ANTHROPIC_API_KEY"]) {
		t.Errorf("SECRET LEAK: the boot error echoes the value: %q", err)
	}
	if _, ok := os.LookupEnv("ANTHROPIC_API_KEY"); ok {
		t.Error("ANTHROPIC_API_KEY was materialized despite the refusal")
	}
}

// TestMaterialize_RefusesSecrets covers the SECOND barrier: even a RuntimeConfig
// constructed directly in Go (bypassing the file loader) cannot push secret
// material into the process environment.
func TestMaterialize_RefusesSecrets(t *testing.T) {
	isolateEnv(t, append(secretCanaryKeys(), "AISOTROPY_ADDR")...)

	c := RuntimeConfig{
		Addr:            ":9191",
		AnthropicAPIKey: secretCanaries["ANTHROPIC_API_KEY"],
		JWTSecret:       secretCanaries["AISOTROPY_JWT_SECRET"],
		OpsToken:        secretCanaries["AISOTROPY_OPS_TOKEN"],
		QdrantAPIKey:    secretCanaries["QDRANT_API_KEY"],
		EmbedAPIKey:     secretCanaries["AISOTROPY_EMBED_API_KEY"],
	}

	applied, err := c.materialize()
	if err == nil {
		t.Fatal("materialize(config with secrets) = nil, want a refusal")
	}
	if !errors.Is(err, ErrSecretInConfigFile) {
		t.Errorf("error %v does not wrap ErrSecretInConfigFile", err)
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil (an all-or-nothing refusal)", applied)
	}
	// Every secret key must be named, and no value may appear.
	msg := err.Error()
	for envKey, val := range secretCanaries {
		if !strings.Contains(msg, envKey) {
			t.Errorf("error %q does not name %q", msg, envKey)
		}
		if strings.Contains(msg, val) {
			t.Errorf("SECRET LEAK: the error echoes the value of %q: %q", envKey, msg)
		}
		if _, ok := os.LookupEnv(envKey); ok {
			t.Errorf("%s reached the environment despite the refusal", envKey)
		}
	}
	// Not even the non-secret keys of a poisoned config are applied.
	if _, ok := os.LookupEnv("AISOTROPY_ADDR"); ok {
		t.Error("AISOTROPY_ADDR was applied from a config that also carried secrets")
	}
}

// TestSecretEnvKeys_CoversEverySecretField keeps secretEnvKeys and the struct in
// sync in the OTHER direction from the drift guard: every canary key this test
// file knows about must still be a real, registered secret key that envMap emits.
// It also re-asserts the residual logging backstop (names only, tagged "(secret)").
func TestSecretEnvKeys_CoversEverySecretField(t *testing.T) {
	c := RuntimeConfig{
		AnthropicAPIKey: secretCanaries["ANTHROPIC_API_KEY"],
		JWTSecret:       secretCanaries["AISOTROPY_JWT_SECRET"],
		OpsToken:        secretCanaries["AISOTROPY_OPS_TOKEN"],
		QdrantAPIKey:    secretCanaries["QDRANT_API_KEY"],
		EmbedAPIKey:     secretCanaries["AISOTROPY_EMBED_API_KEY"],
	}
	em := c.envMap()
	for key, val := range secretCanaries {
		if got, ok := em[key]; !ok {
			t.Errorf("envMap() is missing secret key %q — the refusal could not detect it", key)
		} else if got != val {
			t.Errorf("envMap()[%q] = %q, want %q", key, got, val)
		}
		if !secretEnvKeys[key] {
			t.Errorf("secretEnvKeys is missing %q — a file could supply it", key)
		}
	}
	// secretKeysSet must name every one of them, and nothing else.
	if got := c.secretKeysSet(); len(got) != len(secretCanaries) {
		t.Errorf("secretKeysSet() = %v, want %d keys", got, len(secretCanaries))
	}
	// Residual logging backstop: names only, secrets tagged, values never present.
	logged := strings.Join(redactSecretKeys(secretCanaryKeys()), " ")
	for key, val := range secretCanaries {
		if strings.Contains(logged, val) {
			t.Errorf("redacted log payload leaks the value of %q: %q", key, logged)
		}
		if !strings.Contains(logged, key+" (secret)") {
			t.Errorf("redacted log payload does not tag %q as (secret): %q", key, logged)
		}
	}
}

// nonSecretSecretishEnvKeys is the explicit allowlist for env keys whose NAME
// trips the secret-ish regexp but which carry no secret material. Every entry
// needs a written reason; an unexplained entry is how the drift guard rots.
var nonSecretSecretishEnvKeys = map[string]string{
	"AISOTROPY_JWT_PUBLIC_KEY": "an RSA/EC PUBLIC key in PEM form — published by design " +
		"(it is what token holders verify against); disclosing it grants nothing",
	"AISOTROPY_JWT_PUBLIC_KEY_FILE": "a filesystem PATH to the public key above, not key " +
		"material of any kind",
}

// secretishEnvKeyRe matches env-var names that LOOK like they carry credentials.
// Anything matching it must be deliberately classified: registered in
// secretEnvKeys (refused from the file) or allowlisted above with a reason.
var secretishEnvKeyRe = regexp.MustCompile(`(?i)(KEY|SECRET|TOKEN|PASSWORD|CREDENTIAL)`)

// allEnvKeysOfRuntimeConfig derives, BY REFLECTION over RuntimeConfig, every
// environment variable the type can materialize. It populates each field in turn
// with a type-appropriate value and reads the key back out of envMap, so the list
// is generated from the CODE and cannot go stale as the schema grows — which is
// the whole point of the drift guard built on top of it.
func allEnvKeysOfRuntimeConfig(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(RuntimeConfig{})
	var keys []string
	for i := 0; i < rt.NumField(); i++ {
		v := reflect.New(rt).Elem()
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Slice:
			f.Set(reflect.ValueOf([]string{"x"}))
		case reflect.Pointer:
			p := reflect.New(f.Type().Elem())
			switch p.Elem().Kind() {
			case reflect.Bool:
				p.Elem().SetBool(true)
			case reflect.Int:
				p.Elem().SetInt(1)
			case reflect.Float64:
				p.Elem().SetFloat(1)
			default:
				t.Fatalf("field %s: unhandled pointer element kind %s — extend this helper",
					rt.Field(i).Name, p.Elem().Kind())
			}
			f.Set(p)
		default:
			t.Fatalf("field %s: unhandled kind %s — extend this helper",
				rt.Field(i).Name, f.Kind())
		}
		em := v.Interface().(RuntimeConfig).envMap()
		if len(em) == 0 {
			t.Errorf("field %s materializes no env var — it is dead config surface",
				rt.Field(i).Name)
		}
		for k := range em {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// TestSecretishEnvKeys_AreClassified is the DRIFT GUARD, and it is deliberately
// broader than secretEnvKeys itself. The listed five keys are safe today because
// someone thought about them; the risk this test addresses is the SIXTH — a knob
// added later (say `webhook_signing_key` or `admin_password`) that is secret-ish
// in substance but never gets registered, and so silently becomes file-loadable
// and log-printable again.
//
// It enumerates every env var RuntimeConfig can materialize (by reflection over
// the struct, so nothing can hide from it) and requires each name matching
// (?i)(KEY|SECRET|TOKEN|PASSWORD|CREDENTIAL) to be EXPLICITLY classified: either
// registered in secretEnvKeys — hence refused from config files — or listed in
// nonSecretSecretishEnvKeys with a written reason. Silence is not an option.
func TestSecretishEnvKeys_AreClassified(t *testing.T) {
	for _, k := range allEnvKeysOfRuntimeConfig(t) {
		if !secretishEnvKeyRe.MatchString(k) {
			continue
		}
		if secretEnvKeys[k] {
			continue // classified as secret: refused from config files
		}
		if reason, ok := nonSecretSecretishEnvKeys[k]; ok {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%q is allowlisted as non-secret with an EMPTY reason", k)
			}
			continue
		}
		t.Errorf("env key %q looks secret-bearing but is not classified: add it to "+
			"secretEnvKeys (so config files refuse it) or to nonSecretSecretishEnvKeys "+
			"with a written reason why it carries no secret material", k)
	}

	// The allowlist itself must not rot: an entry for a key the struct no longer
	// materializes is stale and hides nothing, so it must be removed.
	all := map[string]bool{}
	for _, k := range allEnvKeysOfRuntimeConfig(t) {
		all[k] = true
	}
	for k := range nonSecretSecretishEnvKeys {
		if !all[k] {
			t.Errorf("nonSecretSecretishEnvKeys lists %q, which RuntimeConfig no longer "+
				"materializes — remove the stale entry", k)
		}
	}
	for k := range secretEnvKeys {
		if !all[k] {
			t.Errorf("secretEnvKeys lists %q, which RuntimeConfig no longer materializes "+
				"— remove the stale entry", k)
		}
	}
}
