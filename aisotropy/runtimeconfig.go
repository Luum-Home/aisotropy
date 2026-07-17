package aisotropy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/goccy/go-yaml"
)

// RuntimeConfig is the optional runtime config FILE surface. Where the wiring
// Config (config.go, ADR-0016 lever #5) carries deployment VALUES that map to Go
// builder options, RuntimeConfig mirrors the existing AISOTROPY_* / service ENV
// VARS one-for-one — the same knobs the standalone binary and library have always
// read from the environment (see aisotropy/app.go, aisotropy/authenv.go). It does
// NOT invent a new schema: every field corresponds to exactly one environment
// variable, and its only job is to let an operator set those variables from a
// YAML or TOML file instead of the process environment.
//
// PRECEDENCE (see ADR-0040): explicit ENV VAR > config-file value > built-in
// default. A field from the file is materialized into the process environment
// ONLY when that variable is not already present in the environment, so an
// explicit env var always wins and the downstream *FromEnv resolvers keep their
// built-in defaults for anything neither the env nor the file supplies. An app
// with no config file materializes nothing and therefore behaves byte-for-byte
// as it did before this file existed (100% backward compatible).
//
// SECRETS: the secret-bearing fields (AnthropicAPIKey, JWTSecret, OpsToken,
// QdrantAPIKey, EmbedAPIKey) are loadable from the file for local/dev
// convenience, but the environment or a dedicated secret manager remains the
// PREFERRED source in production — a checked-in config file must never carry
// them. materialize never logs secret VALUES, only the names of applied keys.
//
// The file format is chosen by extension: ".toml" is parsed as TOML
// (github.com/BurntSushi/toml, MIT); ".yaml", ".yml", ".json", or anything else
// is parsed as YAML (github.com/goccy/go-yaml — a JSON file also parses, YAML
// being a JSON superset). Pointer fields (*bool/*int/*float64) distinguish
// "absent" (nil, leaves the env/default untouched) from a zero value the operator
// set on purpose.
type RuntimeConfig struct {
	// --- Server / listener ---
	Addr             string   `yaml:"addr" toml:"addr"`                           // AISOTROPY_ADDR
	RequireTLS       *bool    `yaml:"require_tls" toml:"require_tls"`             // AISOTROPY_REQUIRE_TLS
	OpsToken         string   `yaml:"ops_token" toml:"ops_token"`                 // AISOTROPY_OPS_TOKEN (secret)
	DataDir          string   `yaml:"data_dir" toml:"data_dir"`                   // AISOTROPY_DATA_DIR
	PluginDir        string   `yaml:"plugin_dir" toml:"plugin_dir"`               // AISOTROPY_PLUGIN_DIR
	DedicatedTenants []string `yaml:"dedicated_tenants" toml:"dedicated_tenants"` // AISOTROPY_DEDICATED_TENANTS (CSV)
	SleepcycleBin    string   `yaml:"sleepcycle_bin" toml:"sleepcycle_bin"`       // AISOTROPY_SLEEPCYCLE_BIN
	BitrateBin       string   `yaml:"bitrate_bin" toml:"bitrate_bin"`             // AISOTROPY_BITRATE_BIN

	// --- Rate limiting / cost shaping ---
	RatePerSec    *float64 `yaml:"rate_per_sec" toml:"rate_per_sec"`       // AISOTROPY_RATE_PER_SEC
	Burst         *int     `yaml:"burst" toml:"burst"`                     // AISOTROPY_BURST
	CostBudgetUSD *float64 `yaml:"cost_budget_usd" toml:"cost_budget_usd"` // AISOTROPY_COST_BUDGET_USD

	// --- Memory tiers / model prefs ---
	SemanticTurnTTL   string   `yaml:"semantic_turn_ttl" toml:"semantic_turn_ttl"`     // AISOTROPY_SEMANTIC_TURN_TTL (Go duration)
	SemanticTurnMax   *int     `yaml:"semantic_turn_max" toml:"semantic_turn_max"`     // AISOTROPY_SEMANTIC_TURN_MAX
	FactMinImportance *float64 `yaml:"fact_min_importance" toml:"fact_min_importance"` // AISOTROPY_FACT_MIN_IMPORTANCE
	VisionModel       string   `yaml:"vision_model" toml:"vision_model"`               // AISOTROPY_VISION_MODEL
	EmbedURL          string   `yaml:"embed_url" toml:"embed_url"`                     // AISOTROPY_EMBED_URL
	EmbedAPIKey       string   `yaml:"embed_api_key" toml:"embed_api_key"`             // AISOTROPY_EMBED_API_KEY (secret)

	// --- Guardrails ---
	GuardrailsBlockOutput    *bool `yaml:"guardrails_block_output" toml:"guardrails_block_output"`       // AISOTROPY_GUARDRAILS_BLOCK_OUTPUT
	GuardrailsBlockInjection *bool `yaml:"guardrails_block_injection" toml:"guardrails_block_injection"` // AISOTROPY_GUARDRAILS_BLOCK_INJECTION
	GuardrailsLLMJudge       *bool `yaml:"guardrails_llm_judge" toml:"guardrails_llm_judge"`             // AISOTROPY_GUARDRAILS_LLM_JUDGE

	// --- LLM provider ---
	AnthropicAPIKey string `yaml:"anthropic_api_key" toml:"anthropic_api_key"` // ANTHROPIC_API_KEY (secret)

	// --- Facts (rqlite) / Semantic (Qdrant) tier URLs ---
	RqliteURL        string `yaml:"rqlite_url" toml:"rqlite_url"`               // RQLITE_URL
	QdrantHost       string `yaml:"qdrant_host" toml:"qdrant_host"`             // QDRANT_HOST
	QdrantPort       *int   `yaml:"qdrant_port" toml:"qdrant_port"`             // QDRANT_PORT
	QdrantCollection string `yaml:"qdrant_collection" toml:"qdrant_collection"` // QDRANT_COLLECTION
	QdrantAPIKey     string `yaml:"qdrant_api_key" toml:"qdrant_api_key"`       // QDRANT_API_KEY (secret)
	QdrantUseTLS     *bool  `yaml:"qdrant_use_tls" toml:"qdrant_use_tls"`       // QDRANT_USE_TLS

	// --- Auth / JWT verification (ADR-0024) ---
	JWTSecret        string `yaml:"jwt_secret" toml:"jwt_secret"`                   // AISOTROPY_JWT_SECRET (secret)
	JWTPublicKey     string `yaml:"jwt_public_key" toml:"jwt_public_key"`           // AISOTROPY_JWT_PUBLIC_KEY
	JWTPublicKeyFile string `yaml:"jwt_public_key_file" toml:"jwt_public_key_file"` // AISOTROPY_JWT_PUBLIC_KEY_FILE
	JWTJWKSURL       string `yaml:"jwt_jwks_url" toml:"jwt_jwks_url"`               // AISOTROPY_JWT_JWKS_URL
	JWTAudience      string `yaml:"jwt_audience" toml:"jwt_audience"`               // AISOTROPY_JWT_AUDIENCE
	JWTIssuer        string `yaml:"jwt_issuer" toml:"jwt_issuer"`                   // AISOTROPY_JWT_ISSUER
	JWTMaxTTL        string `yaml:"jwt_max_ttl" toml:"jwt_max_ttl"`                 // AISOTROPY_JWT_MAX_TTL
}

// configFileEnv is the name of the env var that points at a runtime config file.
// It is the pointer to the file, not one of the mirrored knobs, so it is never
// materialized from the file itself.
const configFileEnv = "AISOTROPY_CONFIG_FILE"

// defaultConfigSearchPaths are the relative paths LoadAndMaterializeRuntimeConfig
// probes (in order) when AISOTROPY_CONFIG_FILE is unset. The ".runtime." infix
// deliberately distinguishes this env-knob file from the wiring config file
// (config.go), whose conventional name is "aisotropy.yaml" — the two schemas are
// different and must not collide on the same default filename.
var defaultConfigSearchPaths = []string{
	"aisotropy.runtime.yaml",
	"aisotropy.runtime.yml",
	"aisotropy.runtime.toml",
}

// secretEnvKeys are the materialized keys whose VALUES must never be logged. Only
// their names may appear in logs.
var secretEnvKeys = map[string]bool{
	"ANTHROPIC_API_KEY":       true,
	"AISOTROPY_JWT_SECRET":    true,
	"AISOTROPY_OPS_TOKEN":     true,
	"QDRANT_API_KEY":          true,
	"AISOTROPY_EMBED_API_KEY": true,
}

// LoadRuntimeConfigFile reads and parses a runtime config file, selecting YAML or
// TOML by the path's extension. A missing file, unreadable file, or malformed
// (syntactically invalid) file returns a clear, wrapped error — never a panic.
func LoadRuntimeConfigFile(path string) (RuntimeConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("aisotropy: read runtime config %q: %w", path, err)
	}
	var c RuntimeConfig
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		if err := toml.Unmarshal(b, &c); err != nil {
			return RuntimeConfig{}, fmt.Errorf("aisotropy: parse TOML runtime config %q: %w", path, err)
		}
	default: // .yaml, .yml, .json, or unknown -> YAML (a JSON superset)
		if err := yaml.Unmarshal(b, &c); err != nil {
			return RuntimeConfig{}, fmt.Errorf("aisotropy: parse YAML runtime config %q: %w", path, err)
		}
	}
	return c, nil
}

// envMap returns the environment variables this config sets, keyed by env-var
// name, including ONLY fields the operator actually populated (non-empty strings,
// non-nil pointers, non-empty slices). A zero-value config returns an empty map,
// so it materializes nothing.
func (c RuntimeConfig) envMap() map[string]string {
	m := map[string]string{}
	setStr := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	setBool := func(k string, v *bool) {
		if v != nil {
			m[k] = strconv.FormatBool(*v)
		}
	}
	setInt := func(k string, v *int) {
		if v != nil {
			m[k] = strconv.Itoa(*v)
		}
	}
	setFloat := func(k string, v *float64) {
		if v != nil {
			m[k] = strconv.FormatFloat(*v, 'f', -1, 64)
		}
	}

	setStr("AISOTROPY_ADDR", c.Addr)
	setBool("AISOTROPY_REQUIRE_TLS", c.RequireTLS)
	setStr("AISOTROPY_OPS_TOKEN", c.OpsToken)
	setStr("AISOTROPY_DATA_DIR", c.DataDir)
	setStr("AISOTROPY_PLUGIN_DIR", c.PluginDir)
	if len(c.DedicatedTenants) > 0 {
		setStr("AISOTROPY_DEDICATED_TENANTS", strings.Join(c.DedicatedTenants, ","))
	}
	setStr("AISOTROPY_SLEEPCYCLE_BIN", c.SleepcycleBin)
	setStr("AISOTROPY_BITRATE_BIN", c.BitrateBin)

	setFloat("AISOTROPY_RATE_PER_SEC", c.RatePerSec)
	setInt("AISOTROPY_BURST", c.Burst)
	setFloat("AISOTROPY_COST_BUDGET_USD", c.CostBudgetUSD)

	setStr("AISOTROPY_SEMANTIC_TURN_TTL", c.SemanticTurnTTL)
	setInt("AISOTROPY_SEMANTIC_TURN_MAX", c.SemanticTurnMax)
	setFloat("AISOTROPY_FACT_MIN_IMPORTANCE", c.FactMinImportance)
	setStr("AISOTROPY_VISION_MODEL", c.VisionModel)
	setStr("AISOTROPY_EMBED_URL", c.EmbedURL)
	setStr("AISOTROPY_EMBED_API_KEY", c.EmbedAPIKey)

	setBool("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT", c.GuardrailsBlockOutput)
	setBool("AISOTROPY_GUARDRAILS_BLOCK_INJECTION", c.GuardrailsBlockInjection)
	setBool("AISOTROPY_GUARDRAILS_LLM_JUDGE", c.GuardrailsLLMJudge)

	setStr("ANTHROPIC_API_KEY", c.AnthropicAPIKey)

	setStr("RQLITE_URL", c.RqliteURL)
	setStr("QDRANT_HOST", c.QdrantHost)
	setInt("QDRANT_PORT", c.QdrantPort)
	setStr("QDRANT_COLLECTION", c.QdrantCollection)
	setStr("QDRANT_API_KEY", c.QdrantAPIKey)
	setBool("QDRANT_USE_TLS", c.QdrantUseTLS)

	setStr("AISOTROPY_JWT_SECRET", c.JWTSecret)
	setStr("AISOTROPY_JWT_PUBLIC_KEY", c.JWTPublicKey)
	setStr("AISOTROPY_JWT_PUBLIC_KEY_FILE", c.JWTPublicKeyFile)
	setStr("AISOTROPY_JWT_JWKS_URL", c.JWTJWKSURL)
	setStr("AISOTROPY_JWT_AUDIENCE", c.JWTAudience)
	setStr("AISOTROPY_JWT_ISSUER", c.JWTIssuer)
	setStr("AISOTROPY_JWT_MAX_TTL", c.JWTMaxTTL)

	return m
}

// materialize writes each populated field into the process environment, but ONLY
// for variables not already present there — so an explicit env var always wins
// over the file (env > file > default). It returns the sorted names of the keys
// it actually set (never the values), for logging and test assertions.
func (c RuntimeConfig) materialize() ([]string, error) {
	var applied []string
	for k, v := range c.envMap() {
		if _, present := os.LookupEnv(k); present {
			continue // explicit env wins; leave it untouched
		}
		if err := os.Setenv(k, v); err != nil {
			return applied, fmt.Errorf("aisotropy: set env %q from config: %w", k, err)
		}
		applied = append(applied, k)
	}
	sort.Strings(applied)
	return applied, nil
}

// resolveConfigPath returns the config file path to load, honoring (in order):
// an explicit path argument, the AISOTROPY_CONFIG_FILE env var, then the first of
// defaultConfigSearchPaths that exists on disk. The bool is false when no config
// file is configured or found, which callers treat as a clean no-op.
func resolveConfigPath(explicit string) (string, bool) {
	if explicit != "" {
		return explicit, true
	}
	if p := strings.TrimSpace(os.Getenv(configFileEnv)); p != "" {
		return p, true
	}
	for _, p := range defaultConfigSearchPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

// LoadAndMaterializeRuntimeConfig resolves the runtime config file (from the
// explicit path, else AISOTROPY_CONFIG_FILE, else the default search paths),
// loads it, and materializes its values into the process environment under the
// env > file > default precedence. When no config file is configured or found it
// is a clean no-op that returns (nil, nil). A malformed file returns a clear
// error. It is safe to call more than once: already-present env vars are left
// untouched, so a second call is idempotent.
func LoadAndMaterializeRuntimeConfig(explicit string) ([]string, error) {
	path, ok := resolveConfigPath(explicit)
	if !ok {
		return nil, nil
	}
	c, err := LoadRuntimeConfigFile(path)
	if err != nil {
		return nil, err
	}
	applied, err := c.materialize()
	if err != nil {
		return nil, err
	}
	if len(applied) > 0 {
		slog.Info("runtime config file applied", "path", path, "keys", redactSecretKeys(applied))
	}
	return applied, nil
}

// applyConfigFile loads and materializes the App's runtime config file exactly
// once, memoizing the outcome in configErr so repeated startRuntime calls (Run
// then Turn, say) neither re-read the file nor race on the process environment.
// With no config file configured it is a clean no-op.
func (a *App) applyConfigFile() error {
	a.configOnce.Do(func() {
		_, a.configErr = LoadAndMaterializeRuntimeConfig(a.configFile)
	})
	return a.configErr
}

// redactSecretKeys marks secret-bearing key names so a log line shows that a
// secret was supplied by the file without ever revealing its value or even
// confirming which secret store it maps to beyond the (public) var name.
func redactSecretKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if secretEnvKeys[k] {
			out[i] = k + " (secret)"
		} else {
			out[i] = k
		}
	}
	return out
}
