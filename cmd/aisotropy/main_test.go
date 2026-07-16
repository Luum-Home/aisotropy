package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validManifest is a minimal manifest that passes internal/plugin.Validate: a
// kind:go-import plugin (no run.command needed) with a permissive license and one
// scope-gated tool. Used across the round-trip tests.
const validManifest = `name: word-tools
version: 1.2.3
author: Test Author <test@example.com>
license: MIT
description: Counts words in text.
kind: go-import
trust: trusted-inproc
tools:
  - name: word_count
    description: Count the words in a piece of text.
    required_scope: text:analyze
`

// writeManifest drops a manifest file into a fresh temp source directory and
// returns its path.
func writeManifest(t *testing.T, contents string) string {
	t.Helper()
	srcDir := t.TempDir()
	p := filepath.Join(srcDir, "aisotropy-plugin.yaml")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("write source manifest: %v", err)
	}
	return p
}

// TestPluginsRoundTrip exercises the core acceptance path: add a manifest into a
// temp plugin directory, confirm list shows it with the expected columns, then
// remove it and confirm the directory is empty again.
func TestPluginsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeManifest(t, validManifest)

	// add
	var out, errb bytes.Buffer
	if code := run([]string{"plugins", "add", "-dir", dir, manifestPath}, &out, &errb); code != 0 {
		t.Fatalf("add exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "installed word-tools v1.2.3") {
		t.Fatalf("add stdout missing install line: %q", out.String())
	}

	// The manifest file must now exist under the plugin dir, named by plugin name.
	installedPath := filepath.Join(dir, "word-tools-plugin.yaml")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("expected installed manifest at %s: %v", installedPath, err)
	}

	// list
	out.Reset()
	errb.Reset()
	if code := run([]string{"plugins", "list", "-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, errb.String())
	}
	listOut := out.String()
	for _, want := range []string{"NAME", "VERSION", "KIND", "TRUST", "TOOLS", "word-tools", "1.2.3", "go-import", "trusted-inproc"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("list output missing %q:\n%s", want, listOut)
		}
	}
	// The single data row ends with the tool count (1).
	if !strings.HasSuffix(strings.TrimRight(listOut, "\n"), " 1") {
		t.Fatalf("list output should report 1 tool in the last column:\n%s", listOut)
	}

	// remove
	out.Reset()
	errb.Reset()
	if code := run([]string{"plugins", "remove", "-dir", dir, "word-tools"}, &out, &errb); code != 0 {
		t.Fatalf("remove exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "removed word-tools") {
		t.Fatalf("remove stdout missing removal line: %q", out.String())
	}
	if _, err := os.Stat(installedPath); !os.IsNotExist(err) {
		t.Fatalf("expected manifest removed at %s, stat err=%v", installedPath, err)
	}

	// list again -> empty
	out.Reset()
	errb.Reset()
	if code := run([]string{"plugins", "list", "-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("final list exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "no plugins installed") {
		t.Fatalf("expected empty registry notice, got: %q", out.String())
	}
}

// TestAddRejectsInvalidManifest confirms the CLI leans on internal/plugin's
// validation: a copyleft license is refused and nothing is written to the dir.
func TestAddRejectsInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	bad := writeManifest(t, strings.Replace(validManifest, "license: MIT", "license: GPL-3.0", 1))

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "add", "-dir", dir, bad}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit for copyleft license, got 0; stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "not permitted") {
		t.Fatalf("expected license rejection message, got stderr=%q", errb.String())
	}
	// Nothing should have been installed.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("expected empty plugin dir after rejected add, got %d entries", len(entries))
	}
}

// TestRemoveUnknownPlugin confirms remove reports a clear error (non-zero) when
// the named plugin is not installed.
func TestRemoveUnknownPlugin(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"plugins", "remove", "-dir", dir, "does-not-exist"}, &out, &errb)
	if code != 1 {
		t.Fatalf("expected exit 1 for unknown plugin, got %d", code)
	}
	if !strings.Contains(errb.String(), "no installed plugin named") {
		t.Fatalf("expected not-found message, got stderr=%q", errb.String())
	}
}

// TestAddFromDirectory confirms `add <dir>` installs every valid manifest a
// directory contains, reusing the same tolerant scan `list` uses.
func TestAddFromDirectory(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "aisotropy-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	second := strings.Replace(validManifest, "name: word-tools", "name: char-tools", 1)
	if err := os.WriteFile(filepath.Join(srcDir, "char-plugin.yaml"), []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"plugins", "add", "-dir", dir, srcDir}, &out, &errb); code != 0 {
		t.Fatalf("add dir exit=%d stderr=%q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"plugins", "list", "-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, errb.String())
	}
	for _, want := range []string{"word-tools", "char-tools"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list missing %q after add-from-dir:\n%s", want, out.String())
		}
	}
}

// TestResolvePluginDir covers the precedence: -dir flag > env > home default.
func TestResolvePluginDir(t *testing.T) {
	// Explicit flag wins over env.
	t.Setenv("AISOTROPY_PLUGIN_DIR", "/from/env")
	got, err := resolvePluginDir("/from/flag")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/from/flag" {
		t.Fatalf("flag should win, got %q", got)
	}
	// Env used when flag empty.
	got, err = resolvePluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/from/env" {
		t.Fatalf("env should be used, got %q", got)
	}
	// Home default when both empty.
	t.Setenv("AISOTROPY_PLUGIN_DIR", "")
	got, err = resolvePluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(".aisotropy", "plugins")) {
		t.Fatalf("home default expected, got %q", got)
	}
}
