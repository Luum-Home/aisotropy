package plugin

import (
	"strings"
	"testing"
)

const validMCPManifest = `
name: filesystem
version: 1.2.0
author: Aisotropy
license: Apache-2.0
description: Filesystem tools over MCP
kind: mcp
trust: sandboxed
run:
  command: npx
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
tools:
  - name: read_file
    description: Read a file
    required_scope: read:files
personas:
  - support
`

func TestParseManifestBytes_Valid(t *testing.T) {
	m, err := ParseManifestBytes([]byte(validMCPManifest))
	if err != nil {
		t.Fatalf("ParseManifestBytes: unexpected error: %v", err)
	}
	if m.Name != "filesystem" || m.Version != "1.2.0" {
		t.Fatalf("name/version = %q/%q, want filesystem/1.2.0", m.Name, m.Version)
	}
	if m.Kind != KindMCP {
		t.Fatalf("kind = %q, want %q", m.Kind, KindMCP)
	}
	if m.Trust != TrustSandboxed {
		t.Fatalf("trust = %q, want %q", m.Trust, TrustSandboxed)
	}
	if m.Run.Command != "npx" || len(m.Run.Args) != 3 {
		t.Fatalf("run = %+v, want npx with 3 args", m.Run)
	}
	if len(m.Tools) != 1 || m.Tools[0].RequiredScope != "read:files" {
		t.Fatalf("tools = %+v, want one tool scoped read:files", m.Tools)
	}
	if len(m.Personas) != 1 || m.Personas[0] != "support" {
		t.Fatalf("personas = %+v, want [support]", m.Personas)
	}
}

func TestValidate_RejectsCopyleftLicense(t *testing.T) {
	// AGPL is the canonical copyleft the repo-wide gate blocks: a plugin whose
	// tools the core would surface must clear the same permissive bar as any dep.
	for _, lic := range []string{"AGPL-3.0", "GPL-3.0", "LGPL-2.1", "MPL-2.0", "BSL-1.1", "SSPL-1.0"} {
		manifest := strings.Replace(validMCPManifest, "license: Apache-2.0", "license: "+lic, 1)
		_, err := ParseManifestBytes([]byte(manifest))
		if err == nil {
			t.Fatalf("license %q: expected rejection, got nil error", lic)
		}
		if !strings.Contains(err.Error(), "not permitted") {
			t.Fatalf("license %q: error = %v, want 'not permitted'", lic, err)
		}
	}
}

func TestValidate_AcceptsPermissiveLicenseVariants(t *testing.T) {
	for _, lic := range []string{"MIT", "Apache 2.0", "apache-2.0", "BSD-3-Clause", "ISC", "bsd-2-clause"} {
		manifest := strings.Replace(validMCPManifest, "license: Apache-2.0", "license: "+lic, 1)
		if _, err := ParseManifestBytes([]byte(manifest)); err != nil {
			t.Fatalf("license %q: unexpected rejection: %v", lic, err)
		}
	}
}

func TestValidate_RejectsUnknownKind(t *testing.T) {
	manifest := strings.Replace(validMCPManifest, "kind: mcp", "kind: docker", 1)
	_, err := ParseManifestBytes([]byte(manifest))
	if err == nil {
		t.Fatal("expected rejection of unknown kind, got nil")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("error = %v, want 'unknown kind'", err)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{"missing name", func(s string) string { return strings.Replace(s, "name: filesystem", "name: \"\"", 1) }, "name is required"},
		{"missing version", func(s string) string { return strings.Replace(s, "version: 1.2.0", "version: \"\"", 1) }, "version is required"},
		{"missing license", func(s string) string { return strings.Replace(s, "license: Apache-2.0", "license: \"\"", 1) }, "license is required"},
		{"missing kind", func(s string) string { return strings.Replace(s, "kind: mcp", "kind: \"\"", 1) }, "kind is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifestBytes([]byte(tc.mutate(validMCPManifest)))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidate_MCPRequiresCommand(t *testing.T) {
	m := Manifest{Name: "x", Version: "1", License: "MIT", Kind: KindMCP}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "run.command") {
		t.Fatalf("error = %v, want 'run.command'", err)
	}
}

func TestValidate_GoImportDoesNotRequireCommand(t *testing.T) {
	m := Manifest{
		Name: "calc", Version: "1", License: "MIT", Kind: KindGoImport,
		Tools: []ToolSpec{{Name: "add", RequiredScope: "use:calc"}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("go-import manifest should validate without a command: %v", err)
	}
}

func TestValidate_RejectsUnknownTrust(t *testing.T) {
	m := Manifest{Name: "x", Version: "1", License: "MIT", Kind: KindGoImport, Trust: "yolo"}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "unknown trust") {
		t.Fatalf("error = %v, want 'unknown trust'", err)
	}
}

func TestValidate_RejectsToolWithoutName(t *testing.T) {
	m := Manifest{
		Name: "x", Version: "1", License: "MIT", Kind: KindGoImport,
		Tools: []ToolSpec{{Description: "no name"}},
	}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v, want tools[0] name required", err)
	}
}
