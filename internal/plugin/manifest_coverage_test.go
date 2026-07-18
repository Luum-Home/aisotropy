package plugin

import (
	"path/filepath"
	"testing"
)

// TestParseManifest_MissingFile exercises the os.ReadFile error branch: a
// path that does not exist must fail with a wrapped error naming the path,
// not a panic.
func TestParseManifest_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	_, err := ParseManifest(path)
	if err == nil {
		t.Fatal("expected an error for a missing manifest file")
	}
}

// TestParseManifestBytes_InvalidYAML exercises yaml.Unmarshal's error branch:
// malformed YAML must fail to parse rather than silently zero-valuing the
// Manifest.
func TestParseManifestBytes_InvalidYAML(t *testing.T) {
	_, err := ParseManifestBytes([]byte("kind: [unterminated\nname: {broken"))
	if err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

// TestValidate_WASMRequiresModule exercises the "kind wasm requires
// run.module" branch, the sibling of the already-covered "kind mcp requires
// run.command" check.
func TestValidate_WASMRequiresModule(t *testing.T) {
	m := Manifest{Name: "w", Version: "1", License: "MIT", Kind: KindWASM}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected an error for a wasm manifest with no run.module")
	}
}
