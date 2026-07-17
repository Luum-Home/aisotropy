package main

// Branch coverage for the CLI's command handlers, argument parsers, and error
// messages — driven through the testable run() entrypoint and the small pure
// helpers, over temp dirs, with no network and no sandbox. os.Exit wiring
// (main) is deliberately not exercised.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runCLI drives run() and returns (code, stdout, stderr).
func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunTopLevelDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		inStdout string
		inStderr string
	}{
		{"no args", nil, 2, "", "Usage:"},
		{"help word", []string{"help"}, 0, "operator CLI", ""},
		{"-h flag", []string{"-h"}, 0, "operator CLI", ""},
		{"--help flag", []string{"--help"}, 0, "operator CLI", ""},
		{"unknown command", []string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errb := runCLI(tc.args...)
			if code != tc.wantCode {
				t.Fatalf("code=%d want %d (stderr=%q)", code, tc.wantCode, errb)
			}
			if tc.inStdout != "" && !strings.Contains(out, tc.inStdout) {
				t.Fatalf("stdout %q missing %q", out, tc.inStdout)
			}
			if tc.inStderr != "" && !strings.Contains(errb, tc.inStderr) {
				t.Fatalf("stderr %q missing %q", errb, tc.inStderr)
			}
		})
	}
}

func TestRunPluginsDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		inStderr string
		inStdout string
	}{
		{"no subcommand", []string{"plugins"}, 2, "Usage:", ""},
		{"help subcommand", []string{"plugins", "help"}, 0, "", "plugins add"},
		{"-h subcommand", []string{"plugins", "-h"}, 0, "", "plugins add"},
		{"unknown subcommand", []string{"plugins", "wat"}, 2, `unknown subcommand "wat"`, ""},
		{"bad flag", []string{"plugins", "list", "--nope"}, 2, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errb := runCLI(tc.args...)
			if code != tc.wantCode {
				t.Fatalf("code=%d want %d (stderr=%q)", code, tc.wantCode, errb)
			}
			if tc.inStderr != "" && !strings.Contains(errb, tc.inStderr) {
				t.Fatalf("stderr %q missing %q", errb, tc.inStderr)
			}
			if tc.inStdout != "" && !strings.Contains(out, tc.inStdout) {
				t.Fatalf("stdout %q missing %q", out, tc.inStdout)
			}
		})
	}
}

func TestPluginsAddArgErrors(t *testing.T) {
	dir := t.TempDir()

	// Wrong positional count.
	if code, _, errb := runCLI("plugins", "add", "-dir", dir); code != 2 || !strings.Contains(errb, "exactly one") {
		t.Fatalf("add with no source: code=%d stderr=%q", code, errb)
	}
	if code, _, _ := runCLI("plugins", "add", "-dir", dir, "a", "b"); code != 2 {
		t.Fatalf("add with two sources should be exit 2, got %d", code)
	}

	// Nonexistent source → stat error (exit 1).
	if code, _, errb := runCLI("plugins", "add", "-dir", dir, filepath.Join(dir, "nope.yaml")); code != 1 || !strings.Contains(errb, "stat") {
		t.Fatalf("add nonexistent: code=%d stderr=%q", code, errb)
	}

	// Directory with no manifests → "no valid plugin manifest" (exit 1).
	emptySrc := t.TempDir()
	if code, _, errb := runCLI("plugins", "add", "-dir", dir, emptySrc); code != 1 || !strings.Contains(errb, "no valid plugin manifest") {
		t.Fatalf("add empty dir: code=%d stderr=%q", code, errb)
	}

	// A single malformed manifest FILE → ParseManifest error (exit 1).
	badFile := filepath.Join(t.TempDir(), "broken-plugin.yaml")
	if err := os.WriteFile(badFile, []byte("this: is: not: valid: manifest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI("plugins", "add", "-dir", dir, badFile); code != 1 {
		t.Fatalf("add malformed manifest file should be exit 1, got %d", code)
	}
}

func TestPluginsAddInstallDirUncreatable(t *testing.T) {
	// A valid manifest, but the install -dir sits UNDER a regular file, so
	// os.MkdirAll cannot create it → exit 1 after validation succeeded.
	src := writeManifest(t, validManifest)
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "plugins") // path under a file
	if code, _, errb := runCLI("plugins", "add", "-dir", dir, src); code != 1 || !strings.Contains(errb, "create plugin directory") {
		t.Fatalf("add with uncreatable dir: code=%d stderr=%q", code, errb)
	}
}

func TestPluginsListArgAndScanErrors(t *testing.T) {
	// Extra positional → usage exit 2.
	if code, _, errb := runCLI("plugins", "list", "-dir", t.TempDir(), "extra"); code != 2 || !strings.Contains(errb, "takes no arguments") {
		t.Fatalf("list extra arg: code=%d stderr=%q", code, errb)
	}

	// -dir points at a FILE, so scanManifests' ReadDir fails with a non-IsNotExist
	// error → exit 1.
	f := filepath.Join(t.TempDir(), "iamafile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCLI("plugins", "list", "-dir", f); code != 1 || !strings.Contains(errb, "read plugin directory") {
		t.Fatalf("list on a file dir: code=%d stderr=%q", code, errb)
	}
}

func TestPluginsListSkipsMalformedManifestWithWarning(t *testing.T) {
	dir := t.TempDir()
	// A good manifest plus a malformed one sharing the manifest naming convention:
	// list must warn about the bad one and still show the good one.
	if err := os.WriteFile(filepath.Join(dir, "word-tools-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken-plugin.yaml"), []byte("::: not yaml :::\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb := runCLI("plugins", "list", "-dir", dir)
	if code != 0 {
		t.Fatalf("list should still succeed, code=%d stderr=%q", code, errb)
	}
	if !strings.Contains(out, "word-tools") {
		t.Fatalf("list should show the healthy plugin: %q", out)
	}
	if !strings.Contains(errb, "skipping broken-plugin.yaml") {
		t.Fatalf("list should warn about the malformed manifest: %q", errb)
	}
}

func TestPluginsRemoveArgErrors(t *testing.T) {
	dir := t.TempDir()
	if code, _, errb := runCLI("plugins", "remove", "-dir", dir); code != 2 || !strings.Contains(errb, "exactly one") {
		t.Fatalf("remove no name: code=%d stderr=%q", code, errb)
	}
	if code, _, errb := runCLI("plugins", "remove", "-dir", dir, "   "); code != 2 || !strings.Contains(errb, "must not be empty") {
		t.Fatalf("remove blank name: code=%d stderr=%q", code, errb)
	}
	// scan error via -dir pointing at a file.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI("plugins", "remove", "-dir", f, "word-tools"); code != 1 {
		t.Fatalf("remove with scan error should be exit 1, got %d", code)
	}
}

func TestTrustLabel(t *testing.T) {
	if got := trustLabel(""); got != "-" {
		t.Fatalf("trustLabel(empty) = %q, want -", got)
	}
	if got := trustLabel("   "); got != "-" {
		t.Fatalf("trustLabel(spaces) = %q, want -", got)
	}
	if got := trustLabel("trusted-inproc"); got != "trusted-inproc" {
		t.Fatalf("trustLabel = %q", got)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Acme Word/Tools": "acme-word-tools",
		"word_count.v2":   "word_count.v2",
		"  Trim Me  ":     "trim-me",
		"!!!":             "plugin", // sanitizes to empty → fallback
		"a\\b\tc":         "a-b-c",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Fatalf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCopyFileErrors(t *testing.T) {
	// Read error: source does not exist.
	if err := copyFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest")); err == nil {
		t.Fatal("copyFile should fail reading a missing source")
	}
	// Write error: dest is inside a nonexistent directory.
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, filepath.Join("no", "such", "dir", "dest")); err == nil {
		t.Fatal("copyFile should fail writing into a nonexistent directory")
	}
}

// ---- new subcommand --------------------------------------------------------

func TestRunNewDispatchAndArgErrors(t *testing.T) {
	if code, out, _ := runCLI("new", "help"); code != 0 || !strings.Contains(out, "Scaffolds") {
		t.Fatalf("new help: code=%d out=%q", code, out)
	}
	if code, _, _ := runCLI("new", "somedir", "--nope"); code != 2 {
		t.Fatalf("new with unknown flag should be exit 2, got %d", code)
	}
	if code, _, errb := runCLI("new", "a", "b"); code != 2 || !strings.Contains(errb, "exactly one") {
		t.Fatalf("new with two dirs: code=%d stderr=%q", code, errb)
	}
	// A directory whose base sanitizes to empty and no --module → module-derive error.
	if code, _, errb := runCLI("new", "!!!"); code != 2 || !strings.Contains(errb, "cannot derive a module name") {
		t.Fatalf("new undERIVable module: code=%d stderr=%q", code, errb)
	}
	// Invalid --module → exit 2.
	if code, _, errb := runCLI("new", filepath.Join(t.TempDir(), "bot"), "--module", "/leading-slash"); code != 2 || !strings.Contains(errb, "invalid --module") {
		t.Fatalf("new invalid module: code=%d stderr=%q", code, errb)
	}
}

func TestNewRefusesFileTarget(t *testing.T) {
	// Target path exists but is a FILE (not an empty dir) → exit 1.
	f := filepath.Join(t.TempDir(), "mybot")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCLI("new", f); code != 1 || !strings.Contains(errb, "not a directory") {
		t.Fatalf("new onto a file: code=%d stderr=%q", code, errb)
	}
}

func TestResolveModule(t *testing.T) {
	if m, err := resolveModule("/tmp/mybot", "github.com/me/x"); err != nil || m != "github.com/me/x" {
		t.Fatalf("explicit module: %q err=%v", m, err)
	}
	if _, err := resolveModule("/tmp/mybot", "/bad/"); err == nil {
		t.Fatal("invalid explicit module should error")
	}
	if m, err := resolveModule("/some/path/CoolBot", ""); err != nil || m != "coolbot" {
		t.Fatalf("derived module: %q err=%v", m, err)
	}
	if _, err := resolveModule("!!!", ""); err == nil {
		t.Fatal("underivable module should error")
	}
}

func TestIsValidModulePath(t *testing.T) {
	valid := []string{"mybot", "github.com/me/bot", "a/b/c", "a.b-c_d"}
	for _, m := range valid {
		if !isValidModulePath(m) {
			t.Fatalf("isValidModulePath(%q) = false, want true", m)
		}
	}
	invalid := []string{"", "/leading", "trailing/", "a//b", "  ", "@#$"}
	for _, m := range invalid {
		if isValidModulePath(m) {
			t.Fatalf("isValidModulePath(%q) = true, want false", m)
		}
	}
}

func TestSanitizeModuleToken(t *testing.T) {
	cases := map[string]string{
		"CoolBot":  "coolbot",
		"my bot":   "my-bot",
		"a.b_c-d":  "a.b_c-d",
		"@#$":      "",
		"  Trim  ": "trim",
	}
	for in, want := range cases {
		if got := sanitizeModuleToken(in); got != want {
			t.Fatalf("sanitizeModuleToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderTemplateMissing(t *testing.T) {
	if _, err := renderTemplate("templates/does-not-exist.tmpl", scaffoldData{Module: "x"}); err == nil {
		t.Fatal("renderTemplate should fail for a missing embedded template")
	}
}

// ---- task subcommand -------------------------------------------------------

func TestReorderFlagsFirst(t *testing.T) {
	// "--" terminator: everything after is positional, in order.
	got := reorderFlagsFirst([]string{"repo", "--", "-x", "y"})
	if want := []string{"repo", "-x", "y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder with --: got %v, want %v", got, want)
	}
	// A bare "-" is treated as a positional, not a flag.
	got = reorderFlagsFirst([]string{"-", "repo"})
	if want := []string{"-", "repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder bare dash: got %v, want %v", got, want)
	}
	// A value flag before the positional pulls its value along.
	got = reorderFlagsFirst([]string{"repo", "--prompt", "do it"})
	if want := []string{"--prompt", "do it", "repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder value flag: got %v, want %v", got, want)
	}
	// The "--name=value" self-contained form is not treated as a value flag.
	got = reorderFlagsFirst([]string{"repo", "--prompt=do it"})
	if want := []string{"--prompt=do it", "repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder =form: got %v, want %v", got, want)
	}
}

// TestExecTaskRunRejectsBadProvider lives in task_extra_test.go (build tag
// keybroker) since execTaskRun is compiled only in the keybroker build.
