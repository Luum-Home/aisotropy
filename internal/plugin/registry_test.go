package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoad_ScansManifestsAndIgnoresOthers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "aisotropy-plugin.yaml", validMCPManifest)
	writeFile(t, dir, "calc-plugin.yml", `
name: calc
version: 0.1.0
license: MIT
kind: go-import
tools:
  - name: add
    required_scope: use:calc
`)
	// Non-manifest files must be ignored.
	writeFile(t, dir, "README.md", "# not a manifest")
	writeFile(t, dir, "config.yaml", "addr: :8080")

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reg.List()
	if len(got) != 2 {
		t.Fatalf("discovered %d manifests, want 2: %+v", len(got), got)
	}
	// Lexical scan order: aisotropy-plugin.yaml before calc-plugin.yml.
	if got[0].Name != "filesystem" || got[1].Name != "calc" {
		t.Fatalf("order = [%q, %q], want [filesystem, calc]", got[0].Name, got[1].Name)
	}
}

func TestLoad_EmptyDirIsNotAnError(t *testing.T) {
	reg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load empty dir: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("empty dir yielded %d manifests, want 0", len(reg.List()))
	}
}

func TestLoad_FailsOnInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	// A copyleft license makes this manifest invalid; Load must surface it, not
	// silently drop it.
	writeFile(t, dir, "bad-plugin.yaml", `
name: bad
version: 1
license: AGPL-3.0
kind: mcp
run:
  command: x
`)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected Load to fail on an invalid (copyleft) manifest")
	}
}

func TestIsManifestFile(t *testing.T) {
	cases := map[string]bool{
		"aisotropy-plugin.yaml": true,
		"foo.plugin.yml":        true,
		"PLUGIN.YAML":           true,
		"config.yaml":           false,
		"plugin.txt":            false,
		"README.md":             false,
	}
	for name, want := range cases {
		if got := IsManifestFile(name); got != want {
			t.Errorf("IsManifestFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestList_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "aisotropy-plugin.yaml", validMCPManifest)
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := reg.List()
	first[0].Name = "mutated"
	if reg.List()[0].Name != "filesystem" {
		t.Fatal("List() must return a defensive copy; internal state was mutated")
	}
}
