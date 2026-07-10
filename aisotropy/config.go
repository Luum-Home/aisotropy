package aisotropy

import (
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the declarative WIRING surface (ADR-0016 lever #5), gated by the
// anti-magic rule (ADR-0002): it carries deployment VALUES only — listen address,
// rate limits, cost pricing, literal personas, and MCP server references — and
// NEVER in-process behavior. A tool backed by a Go executor, the LLM client, and
// typed handlers stay Go options (WithToolExecutor / WithTypedTool / WithLLM):
// a YAML file cannot carry a Go func, and a behavior-less tool list in config is
// exactly the "magic" ADR-0002 forbids.
//
// MCP servers ARE allowed here because their behavior lives in the external
// process the config only points at (command + args) — declaring one is wiring a
// reference, not hiding behavior. So a config file plus a `WithToolExecutor` (or
// nothing, if all tools are MCP) is enough to stand up a bot's deployment shape
// without editing Go.
//
// The file is YAML (a JSON file also parses, YAML being a JSON superset). Compose
// it with Go options via OptionsFromConfig / OptionsFromConfigFile.
type Config struct {
	// Addr is the HTTP listen address (maps to WithAddr). Empty = env/default.
	Addr string `yaml:"addr"`
	// RateLimit sets the per-tenant token bucket (maps to WithRateLimit).
	RateLimit *RateLimitConfig `yaml:"rate_limit"`
	// CostPricing sets the /costs USD estimate (maps to WithCostPricing).
	CostPricing *CostPricingConfig `yaml:"cost_pricing"`
	// Personas are per-product literal base prompts (each maps to WithPersona).
	Personas []PersonaConfig `yaml:"personas"`
	// MCPServers are stdio MCP servers to expose (each maps to WithMCPServer).
	MCPServers []MCPServerConfig `yaml:"mcp_servers"`
}

// RateLimitConfig is the per-tenant token-bucket wiring.
type RateLimitConfig struct {
	PerSec float64 `yaml:"per_sec"`
	Burst  float64 `yaml:"burst"`
}

// CostPricingConfig is the per-1M-token USD pricing for the /costs estimate.
type CostPricingConfig struct {
	InputPer1M  float64 `yaml:"input_per_1m"`
	OutputPer1M float64 `yaml:"output_per_1m"`
}

// PersonaConfig is one per-product base prompt. ProductID "" is the default
// persona (used for any product_id without a specific one). BasePrompt is a
// literal string — no templating (ADR-0002).
type PersonaConfig struct {
	ProductID  string `yaml:"product_id"`
	BasePrompt string `yaml:"base_prompt"`
}

// MCPServerConfig references a stdio MCP server whose tools the runtime exposes,
// scope-gated by RequiredScope (empty = always available). The behavior is the
// external process; the config only names it.
type MCPServerConfig struct {
	RequiredScope string   `yaml:"required_scope"`
	Command       string   `yaml:"command"`
	Args          []string `yaml:"args"`
}

// LoadConfigFile reads and validates a YAML (or JSON) wiring config file.
func LoadConfigFile(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("aisotropy: read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("aisotropy: parse config %q: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("aisotropy: invalid config %q: %w", path, err)
	}
	return c, nil
}

// validate rejects nonsensical wiring values so a bad config fails at load, not
// at request time.
func (c Config) validate() error {
	if c.RateLimit != nil && (c.RateLimit.PerSec < 0 || c.RateLimit.Burst < 0) {
		return errors.New("rate_limit: per_sec and burst must be >= 0")
	}
	if c.CostPricing != nil && (c.CostPricing.InputPer1M < 0 || c.CostPricing.OutputPer1M < 0) {
		return errors.New("cost_pricing: input_per_1m and output_per_1m must be >= 0")
	}
	for i, m := range c.MCPServers {
		if m.Command == "" {
			return fmt.Errorf("mcp_servers[%d]: command is required", i)
		}
	}
	return nil
}

// OptionsFromConfig turns a Config into builder Options, in a stable order.
// Compose them with the Go-only options (executors, typed tools, LLM client):
//
//	opts, err := aisotropy.OptionsFromConfigFile("aisotropy.yaml")
//	if err != nil { log.Fatal(err) }
//	app := aisotropy.New(append(opts,
//		aisotropy.WithToolExecutor(myExec),
//		aisotropy.WithTypedTool("get_weather", "...", "read:weather", getWeather),
//	)...)
//
// Because options apply in order and later options of the same kind win, a Go
// option placed after the config options overrides the file (config as defaults,
// code as overrides).
func OptionsFromConfig(c Config) []Option {
	var opts []Option
	if c.Addr != "" {
		opts = append(opts, WithAddr(c.Addr))
	}
	if c.RateLimit != nil {
		opts = append(opts, WithRateLimit(c.RateLimit.PerSec, c.RateLimit.Burst))
	}
	if c.CostPricing != nil {
		opts = append(opts, WithCostPricing(c.CostPricing.InputPer1M, c.CostPricing.OutputPer1M))
	}
	for _, p := range c.Personas {
		opts = append(opts, WithPersona(p.ProductID, Persona{BasePrompt: p.BasePrompt}))
	}
	for _, m := range c.MCPServers {
		opts = append(opts, WithMCPServer(m.RequiredScope, m.Command, m.Args...))
	}
	return opts
}

// OptionsFromConfigFile is LoadConfigFile followed by OptionsFromConfig.
func OptionsFromConfigFile(path string) ([]Option, error) {
	c, err := LoadConfigFile(path)
	if err != nil {
		return nil, err
	}
	return OptionsFromConfig(c), nil
}
