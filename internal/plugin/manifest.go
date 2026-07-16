// Package plugin is the ecosystem scaffolding for third-party capability bundles
// (ADR-0016's "grow the toolset without editing the core" thesis, taken one step
// further than the in-tree MCP/typed-tool options). A *plugin* is a declarative
// manifest — `aisotropy-plugin.yaml` — plus a way to launch the tools it
// describes. The manifest carries governance metadata (name, version, author,
// LICENSE, trust posture) and a scope-gated tool list; the loader turns a
// validated manifest into the exact shapes the runtime already speaks: a
// []tools.Tool list plus map[string]api.TypedToolHandler, every tool gated by its
// declared RequiredScope (the same double scope-gate native and MCP tools get).
//
// Dependency direction (deliberate): internal/plugin imports internal/tools and
// internal/api to reuse those shapes, but NEVER the public aisotropy package —
// the public builder wires plugins in (Wave 2), not the other way round. The only
// runtime capability the loader needs from the outside (how to actually connect
// an MCP subprocess) is injected as a func, so this package stays free of any MCP
// client library and is unit-testable without spawning a process.
//
// Supported kinds today: "mcp" (reuses the existing gollem MCP path) and
// "go-import" (a documented compile-time convention — Go has no runtime plugin
// loading, so the manifest contributes governance-scoped descriptors while the
// behavior arrives via a normal Go import the consumer wires). "wasm" is a
// RESERVED enum value for a later workflow: it validates but the loader returns a
// clear "not yet supported" error rather than pretending to run anything.
package plugin

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
)

// Kind selects how a plugin's tools are launched. It is a closed enum: an
// unrecognized value is rejected at Validate time so a typo fails at load, not at
// request time.
type Kind string

const (
	// KindMCP exposes the plugin's tools by connecting to an MCP server
	// subprocess (Run.Command + Run.Args) and discovering its tools — the
	// polyglot-execution path (ADR-0016 lever #2). Behavior lives in that
	// external process; the runtime keeps owning scoping and identity.
	KindMCP Kind = "mcp"
	// KindGoImport marks a plugin whose tool behavior is compiled Go, registered
	// by importing a package and calling its registration func. Go has no safe
	// cross-platform runtime plugin loading, so this is a documented CONVENTION,
	// not a dynamic load: the manifest supplies governance-scoped descriptors for
	// discovery/surfacing; the handlers arrive via the Go import the consumer
	// already links. See LoadTools for exactly what is (and is not) wired.
	KindGoImport Kind = "go-import"
	// KindWASM is RESERVED for a future WebAssembly-sandboxed execution workflow.
	// It is accepted by Validate (so a manifest can declare it today) but the
	// loader returns ErrKindUnsupported — nothing is executed. Do not treat a
	// wasm plugin as runnable until this is implemented.
	KindWASM Kind = "wasm"
)

// Trust records the isolation posture a deployer intends for the plugin. It is
// metadata the runtime and operators can act on (e.g. refuse to run a
// "trusted-inproc" plugin from an untrusted source); this package validates the
// value but does not itself enforce a sandbox.
type Trust string

const (
	// TrustInproc means the plugin runs with the runtime's own trust — an
	// in-process Go import, or an MCP subprocess treated as trusted.
	TrustInproc Trust = "trusted-inproc"
	// TrustSandboxed means the plugin is expected to run isolated (a constrained
	// subprocess today; a WASM sandbox once KindWASM lands).
	TrustSandboxed Trust = "sandboxed"
)

// Manifest is the parsed `aisotropy-plugin.yaml`. It is the single declarative
// surface for a capability bundle: governance metadata plus a scope-gated tool
// list. It carries VALUES and references only (command, args, scopes) — never
// in-process Go behavior, consistent with the anti-magic rule (ADR-0002): a YAML
// file cannot carry a Go func, so kind:go-import handlers are wired in Go, and
// kind:mcp behavior lives in the external process the manifest merely names.
type Manifest struct {
	// Name is the plugin's stable identifier (required).
	Name string `yaml:"name"`
	// Version is the plugin's own version string, e.g. semver (required so a
	// deployer can pin/audit what they ran).
	Version string `yaml:"version"`
	// Author is a free-form attribution (optional).
	Author string `yaml:"author"`
	// License is the plugin's SPDX-ish license id. It is validated against a
	// permissive allowlist (MIT/BSD/Apache/ISC) — copyleft and source-available
	// licenses are rejected, consistent with the repo-wide license gate
	// (.claude/rules/constitutional-gates.md, license-policy). A plugin the core
	// would surface tools from is effectively part of the deployed surface, so it
	// must clear the same bar as any dependency (required).
	License string `yaml:"license"`
	// Description is a short human summary (optional).
	Description string `yaml:"description"`
	// Kind selects the launch mechanism (required; one of KindMCP/KindGoImport/
	// KindWASM).
	Kind Kind `yaml:"kind"`
	// Trust is the intended isolation posture (optional; validated when set).
	Trust Trust `yaml:"trust"`
	// Run is the subprocess spec for KindMCP (Command required for that kind;
	// ignored for others).
	Run RunSpec `yaml:"run"`
	// Tools declares the plugin's scope-gated tools. For KindMCP this is optional
	// documentation (the server is the source of truth for its own tool list); for
	// KindGoImport it is the governance-scoped descriptor set the loader surfaces.
	Tools []ToolSpec `yaml:"tools"`
	// Personas lists persona/product identifiers the plugin ships prompts for
	// (metadata; wiring personas stays a Go/config concern).
	Personas []string `yaml:"personas"`
}

// RunSpec is the stdio launch command for a KindMCP plugin. The behavior is the
// external process; the manifest only points at it.
type RunSpec struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

// ToolSpec is one declared, scope-gated tool. RequiredScope gates it exactly as
// tools.Tool.RequiredScope does: empty means always available, otherwise the
// granted scopes must include it or the tool is never injected for the model.
type ToolSpec struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	RequiredScope string `yaml:"required_scope"`
}

// permittedLicenses is the normalized allowlist of permissive licenses a plugin
// may declare. Keys are license ids uppercased with spaces removed (see
// normalizeLicense), so "Apache 2.0", "Apache-2.0", and "apache2.0" all match.
// Copyleft (GPL/LGPL/AGPL/MPL) and source-available (BSL/SSPL/ELv2/Commons
// Clause/FSL) licenses are deliberately absent and therefore rejected.
var permittedLicenses = map[string]struct{}{
	"MIT":          {},
	"ISC":          {},
	"BSD":          {},
	"BSD-2-CLAUSE": {},
	"BSD-3-CLAUSE": {},
	"BSD-2":        {},
	"BSD-3":        {},
	"APACHE-2.0":   {},
	"APACHE2.0":    {},
	"APACHE-2":     {},
}

// normalizeLicense folds an id to the allowlist's key form: trim, uppercase, and
// drop interior spaces so spelling/punctuation variants collapse to one key.
func normalizeLicense(license string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(license)), " ", "")
}

// licensePermitted reports whether the declared license clears the permissive
// allowlist. An empty license is not permitted — the field is required so a
// plugin cannot ship with its license unstated.
func licensePermitted(license string) bool {
	_, ok := permittedLicenses[normalizeLicense(license)]
	return ok
}

// ParseManifest reads and validates an `aisotropy-plugin.yaml` file at path. A
// parse or validation failure is returned as an error wrapping the path so a bad
// manifest fails at load, not at request time.
func ParseManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin: read manifest %q: %w", path, err)
	}
	m, err := ParseManifestBytes(b)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin: manifest %q: %w", path, err)
	}
	return m, nil
}

// ParseManifestBytes parses and validates a manifest from raw YAML (a JSON
// document also parses, YAML being a JSON superset). The returned Manifest is
// guaranteed to have passed Validate.
func ParseManifestBytes(b []byte) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("invalid: %w", err)
	}
	return m, nil
}

// Validate rejects a manifest that is missing required fields, declares a
// non-permissive license, uses an unknown kind/trust, or omits the command a
// KindMCP plugin needs. It is the single gate every ParseManifest* path runs, so
// downstream code (registry, loader) can assume a Manifest is well-formed.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version is required")
	}
	if strings.TrimSpace(m.License) == "" {
		return errors.New("license is required")
	}
	if !licensePermitted(m.License) {
		return fmt.Errorf("license %q is not permitted: only permissive licenses (MIT, BSD-2/3-Clause, Apache-2.0, ISC) are allowed — copyleft and source-available licenses are rejected", m.License)
	}
	switch m.Kind {
	case KindMCP, KindGoImport, KindWASM:
		// recognized
	case "":
		return errors.New("kind is required (mcp|go-import|wasm)")
	default:
		return fmt.Errorf("unknown kind %q (want mcp|go-import|wasm)", m.Kind)
	}
	switch m.Trust {
	case "", TrustInproc, TrustSandboxed:
		// empty is allowed (deployer decides); a set value must be valid
	default:
		return fmt.Errorf("unknown trust %q (want trusted-inproc|sandboxed)", m.Trust)
	}
	if m.Kind == KindMCP && strings.TrimSpace(m.Run.Command) == "" {
		return errors.New("kind mcp requires run.command")
	}
	for i, t := range m.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("tools[%d]: name is required", i)
		}
	}
	return nil
}
