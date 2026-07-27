package aisotropy

import (
	"os"
	"path/filepath"
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

	// And the reverse direction: every real field should be documented, so the
	// example stays a complete reference as the schema grows.
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
		if !documentedSet[m[1]] {
			t.Errorf("RuntimeConfig field %q is not documented in %s", m[1], examplePath)
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

// TestSecretEnvKeys_CoversEverySecretField guards the secrets posture against
// DRIFT. Per ADR-0040 §5 secret-bearing fields ARE loadable from the file for
// local/dev convenience, and the safety property that makes that acceptable is
// that their VALUES are never logged — materialize logs key NAMES only, with
// secrets tagged "(secret)" by redactSecretKeys.
//
// That property silently breaks the day someone adds a new secret field to
// RuntimeConfig and forgets to register it in secretEnvKeys: it would then be
// materialized and its name logged UNTAGGED. This test populates every
// secret-bearing field, materializes it, and asserts (a) each secret's env key
// is registered in secretEnvKeys, and (b) no secret VALUE appears anywhere in
// the redacted log payload.
func TestSecretEnvKeys_CoversEverySecretField(t *testing.T) {
	// Every secret-bearing field in RuntimeConfig, with a distinctive value.
	secretFields := map[string]string{
		"ANTHROPIC_API_KEY":       "sk-ant-CANARY-anthropic",
		"AISOTROPY_JWT_SECRET":    "CANARY-jwt-secret",
		"AISOTROPY_OPS_TOKEN":     "CANARY-ops-token",
		"QDRANT_API_KEY":          "CANARY-qdrant-key",
		"AISOTROPY_EMBED_API_KEY": "CANARY-embed-key",
	}
	keys := make([]string, 0, len(secretFields))
	for k := range secretFields {
		keys = append(keys, k)
	}
	isolateEnv(t, keys...)

	c := RuntimeConfig{
		AnthropicAPIKey: secretFields["ANTHROPIC_API_KEY"],
		JWTSecret:       secretFields["AISOTROPY_JWT_SECRET"],
		OpsToken:        secretFields["AISOTROPY_OPS_TOKEN"],
		QdrantAPIKey:    secretFields["QDRANT_API_KEY"],
		EmbedAPIKey:     secretFields["AISOTROPY_EMBED_API_KEY"],
	}

	// Every secret field must actually reach the environment under its documented
	// key — this also catches a field renamed in envMap without updating the list.
	em := c.envMap()
	for key, val := range secretFields {
		if got, ok := em[key]; !ok {
			t.Errorf("envMap() is missing secret key %q", key)
		} else if got != val {
			t.Errorf("envMap()[%q] = %q, want %q", key, got, val)
		}
		if !secretEnvKeys[key] {
			t.Errorf("secretEnvKeys is missing %q — its value would be logged untagged", key)
		}
	}

	applied, err := c.materialize()
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// The log payload must carry NAMES only — never a secret VALUE.
	logged := strings.Join(redactSecretKeys(applied), " ")
	for key, val := range secretFields {
		if strings.Contains(logged, val) {
			t.Errorf("redacted log payload leaks the value of %q: %q", key, logged)
		}
		if !strings.Contains(logged, key+" (secret)") {
			t.Errorf("redacted log payload does not tag %q as (secret): %q", key, logged)
		}
	}
}
