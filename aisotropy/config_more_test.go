package aisotropy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigFile_ReadError verifies the read-error branch: a missing file
// surfaces a wrapped error rather than a zero Config that would silently boot a
// misconfigured runtime.
func TestLoadConfigFile_ReadError(t *testing.T) {
	_, err := LoadConfigFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("LoadConfigFile on a missing file must return an error")
	}
}

// TestLoadConfigFile_ParseError verifies the YAML parse-error branch: malformed
// YAML fails at load, not at request time.
func TestLoadConfigFile_ParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	// A scalar where a mapping is required by the Config struct — invalid YAML
	// document structure for unmarshalling into Config.
	if err := os.WriteFile(path, []byte("addr: [unterminated\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("LoadConfigFile on malformed YAML must return an error")
	}
}

// TestLoadConfigFile_ValidationError verifies the validate() failure is wrapped
// by LoadConfigFile (a bad rate limit rejected at load time).
func TestLoadConfigFile_ValidationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("rate_limit:\n  per_sec: -1\n  burst: 5\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("LoadConfigFile must reject a negative rate limit at load time")
	}
}

// TestOptionsFromConfigFile_PropagatesLoadError verifies OptionsFromConfigFile
// returns the LoadConfigFile error (nil options) rather than swallowing it.
func TestOptionsFromConfigFile_PropagatesLoadError(t *testing.T) {
	opts, err := OptionsFromConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("OptionsFromConfigFile on a missing file must return an error")
	}
	if opts != nil {
		t.Errorf("options = %v, want nil on load error", opts)
	}
}
