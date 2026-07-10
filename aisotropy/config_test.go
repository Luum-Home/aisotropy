package aisotropy

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleConfig = `
addr: ":9090"
rate_limit:
  per_sec: 20
  burst: 40
cost_pricing:
  input_per_1m: 3.0
  output_per_1m: 15.0
personas:
  - product_id: weatherbot
    base_prompt: "You are WeatherBot."
mcp_servers:
  - required_scope: "read:files"
    command: "mcp-server-filesystem"
    args: ["/tmp"]
`

// TestConfigFile_ParsesAndAppliesToApp verifies the full path: a YAML wiring
// file loads, becomes Options, and those Options set the App's fields — so a
// deployment's shape (addr, limits, pricing, personas, MCP servers) is
// declarative, no Go edit (ADR-0016 lever #5).
func TestConfigFile_ParsesAndAppliesToApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aisotropy.yaml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	opts, err := OptionsFromConfigFile(path)
	if err != nil {
		t.Fatalf("OptionsFromConfigFile: %v", err)
	}
	app := New(opts...)

	if app.addr != ":9090" {
		t.Errorf("addr = %q, want :9090", app.addr)
	}
	if app.ratePerSec != 20 || app.burst != 40 {
		t.Errorf("rate limit = %v/%v, want 20/40", app.ratePerSec, app.burst)
	}
	if app.inPricePer1M != 3.0 || app.outPricePer1M != 15.0 {
		t.Errorf("pricing = %v/%v, want 3/15", app.inPricePer1M, app.outPricePer1M)
	}
	if app.personas["weatherbot"] != "You are WeatherBot." {
		t.Errorf("persona = %q, want the configured prompt", app.personas["weatherbot"])
	}
	if len(app.mcpServers) != 1 {
		t.Fatalf("mcpServers = %+v, want 1", app.mcpServers)
	}
	m := app.mcpServers[0]
	if m.command != "mcp-server-filesystem" || m.requiredScope != "read:files" || len(m.args) != 1 || m.args[0] != "/tmp" {
		t.Errorf("mcp server = %+v, want filesystem/read:files/[/tmp]", m)
	}
}

// TestConfig_GoOptionOverridesFile verifies config values are defaults a later
// Go option overrides (config as baseline, code as override).
func TestConfig_GoOptionOverridesFile(t *testing.T) {
	c := Config{Addr: ":9090"}
	app := New(append(OptionsFromConfig(c), WithAddr(":1234"))...)
	if app.addr != ":1234" {
		t.Errorf("addr = %q, want :1234 (Go option must override the file)", app.addr)
	}
}

// TestConfig_ValidationRejectsBadWiring verifies load-time validation of the
// wiring values (fail at load, not at request time).
func TestConfig_ValidationRejectsBadWiring(t *testing.T) {
	cases := map[string]Config{
		"negative rate":       {RateLimit: &RateLimitConfig{PerSec: -1}},
		"negative price":      {CostPricing: &CostPricingConfig{InputPer1M: -1}},
		"mcp missing command": {MCPServers: []MCPServerConfig{{RequiredScope: "s"}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.validate(); err == nil {
				t.Fatalf("validate(%+v) = nil, want an error", c)
			}
		})
	}
}
