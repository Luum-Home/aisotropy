package main

// Additional branch coverage beyond cli_extra_test.go / main_test.go / new_test.go:
// the os.Exit seam, HOME-derivation failure paths, the scanManifests
// not-yet-created-directory and skip-non-manifest branches, copyFile/os.Remove
// failures reached THROUGH the CLI (not the direct unit tests in
// cli_extra_test.go), the scan-warning closures actually firing, and the `new`
// subcommand's scaffold-failure propagation + renderTemplate's own parse/execute
// error branches via the scaffoldTemplatesFS seam.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// ---- main()/exitFunc seam ---------------------------------------------------

// withFakeExit swaps the exitFunc seam for the duration of a test, recording
// every requested exit code instead of terminating the test binary, and
// restores the real os.Exit on cleanup.
func withFakeExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	prev := exitFunc
	exitFunc = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitFunc = prev })
	return &codes
}

// TestMain_Direct drives the real main() end to end via the exitFunc seam,
// covering its os.Args/os.Stdout/os.Stderr wiring without ever calling the
// real os.Exit.
func TestMain_Direct(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"aisotropy", "-h"}

	codes := withFakeExit(t)
	main()
	if len(*codes) != 1 || (*codes)[0] != 0 {
		t.Errorf("exitFunc calls = %v, want exactly one call with code 0 (-h)", *codes)
	}
}

// ---- resolvePluginDir / runPlugins: HOME-derivation failure -----------------

// TestResolvePluginDir_HomeDirUnavailable covers resolvePluginDir's own
// os.UserHomeDir error branch, AND (via run()) runPlugins' handling of that
// error, by clearing every env var os.UserHomeDir consults on Unix.
func TestResolvePluginDir_HomeDirUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir reads the USERPROFILE env var on Windows; this test targets the Unix HOME path")
	}
	t.Setenv("HOME", "")
	t.Setenv("AISOTROPY_PLUGIN_DIR", "")

	if _, err := resolvePluginDir(""); err == nil {
		t.Fatal("resolvePluginDir(\"\") should fail when $HOME is unset and no -dir/env override is given")
	}

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "list"}, &out, &errb)
	if code != 1 {
		t.Errorf("plugins list with no resolvable home dir: code=%d, want 1; stderr=%q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "aisotropy plugins list:") {
		t.Errorf("stderr = %q, want the plugins-list error prefix", errb.String())
	}
}

// ---- scanManifests: not-yet-created directory + skip-non-manifest ----------

// TestPluginsList_DirectoryDoesNotExistYet covers scanManifests' IsNotExist
// branch: a plugin dir that has simply never been created is a valid empty
// state (list must say "no plugins installed", not error).
func TestPluginsList_DirectoryDoesNotExistYet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	code, out, errb := func() (int, string, string) {
		var o, e bytes.Buffer
		c := run([]string{"plugins", "list", "-dir", dir}, &o, &e)
		return c, o.String(), e.String()
	}()
	if code != 0 {
		t.Fatalf("list on a never-created dir: code=%d stderr=%q", code, errb)
	}
	if !strings.Contains(out, "no plugins installed") {
		t.Fatalf("expected the empty-registry notice, got: %q", out)
	}
}

// TestPluginsList_SkipsNonManifestEntries covers scanManifests' "continue"
// branch (a directory entry that is either a subdirectory or does not match
// the manifest naming convention) — distinct from
// TestPluginsListSkipsMalformedManifestWithWarning, which only exercises the
// ParseManifest-failure skip, not the naming-convention/IsDir skip.
func TestPluginsList_SkipsNonManifestEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "word-tools-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray non-manifest file (no "plugin" in the name / wrong extension).
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subdirectory that happens to match the naming convention by name but
	// must still be skipped because it is a directory, not a file.
	if err := os.MkdirAll(filepath.Join(dir, "some-plugin.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "list", "-dir", dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("list: code=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "word-tools") {
		t.Fatalf("expected the real manifest to still be listed: %q", out.String())
	}
	if strings.Contains(out.String(), "README") || strings.Contains(errb.String(), "README") {
		t.Fatalf("README.md should be silently skipped, not surfaced: stdout=%q stderr=%q", out.String(), errb.String())
	}
}

// ---- pluginsAdd: copyFile failure reached through the CLI -------------------

// TestPluginsAdd_CopyFileFailsWhenDestIsADirectory covers pluginsAdd's own
// copyFile-error branch (distinct from TestCopyFileErrors in
// cli_extra_test.go, which calls copyFile directly rather than through `add`):
// the destination filename is pre-occupied by a directory, so the final
// os.WriteFile inside copyFile fails.
func TestPluginsAdd_CopyFileFailsWhenDestIsADirectory(t *testing.T) {
	dir := t.TempDir()
	// word-tools-plugin.yaml is exactly the destination filename pluginsAdd
	// derives from validManifest's "name: word-tools".
	if err := os.MkdirAll(filepath.Join(dir, "word-tools-plugin.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := writeManifest(t, validManifest)

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "add", "-dir", dir, src}, &out, &errb)
	if code != 1 {
		t.Fatalf("add onto a directory-shaped destination: code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "install") {
		t.Fatalf("stderr = %q, want the install-failure message", errb.String())
	}
}

// ---- collectSources / scanManifests: the warn closure actually firing ------

// TestPluginsAdd_DirectoryWarnsAboutMalformedManifest covers
// collectSources' warn-callback closure body (only reachable when `add
// <dir>` scans a directory containing an invalid manifest) — distinct from
// TestPluginsListSkipsMalformedManifestWithWarning, which exercises the
// `list` closure, and TestAddFromDirectory, whose fixture has no malformed
// manifest so the warn body never runs.
func TestPluginsAdd_DirectoryWarnsAboutMalformedManifest(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "word-tools-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "broken-plugin.yaml"), []byte("::: not yaml :::\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"plugins", "add", "-dir", dir, srcDir}, &out, &errb)
	if code != 0 {
		t.Fatalf("add dir with one malformed manifest: code=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "word-tools") {
		t.Fatalf("the valid manifest should still install: %q", out.String())
	}
	if !strings.Contains(errb.String(), "skipping broken-plugin.yaml") {
		t.Fatalf("expected a warning about the malformed manifest, got stderr=%q", errb.String())
	}
}

// TestPluginsRemove_WarnsAboutMalformedManifestWhileScanning covers
// pluginsRemove's warn-callback closure body: a malformed manifest sits
// alongside the target during the scan `remove` performs to find it.
func TestPluginsRemove_WarnsAboutMalformedManifestWhileScanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "word-tools-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken-plugin.yaml"), []byte("::: not yaml :::\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "remove", "-dir", dir, "word-tools"}, &out, &errb)
	if code != 0 {
		t.Fatalf("remove: code=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "skipping broken-plugin.yaml") {
		t.Fatalf("expected a warning about the malformed manifest during remove's scan, got stderr=%q", errb.String())
	}
}

// TestPluginsRemove_FailsWhenParentDirNotWritable covers pluginsRemove's own
// os.Remove-error branch: the matching manifest is found, but its containing
// directory is not writable so the unlink itself fails. Skipped when running
// as root (uid 0), since root bypasses Unix permission checks and the delete
// would unexpectedly succeed.
func TestPluginsRemove_FailsWhenParentDirNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix permission checks")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "word-tools-plugin.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let t.TempDir() clean up afterward

	var out, errb bytes.Buffer
	code := run([]string{"plugins", "remove", "-dir", dir, "word-tools"}, &out, &errb)
	if code != 1 {
		t.Fatalf("remove from a read-only dir: code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "aisotropy plugins remove:") {
		t.Fatalf("stderr = %q, want the remove-error prefix", errb.String())
	}
}

// ---- `new` subcommand: missing <dir> after flags-only args ------------------

// TestRunNew_FlagsOnlyNoPositionalDir covers runNew's "len(rest) == 0 after
// flag parsing" branch — distinct from the len(args)==0 case (no tokens at
// all) already covered by TestRunNewDispatchAndArgErrors.
func TestRunNew_FlagsOnlyNoPositionalDir(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"new", "--module", "somemod"}, &out, &errb)
	if code != 2 {
		t.Fatalf("new with only --module, no <dir>: code=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "missing <dir> argument") {
		t.Fatalf("stderr = %q, want the missing-<dir> message", errb.String())
	}
}

// ---- scaffold()/renderTemplate(): error branches via the fs.FS seam --------

// withScaffoldFixture temporarily overrides both scaffoldFiles and
// scaffoldTemplatesFS for the duration of a test, restoring the real embedded
// templates on cleanup, so a test can exercise renderTemplate/scaffold error
// paths without shipping a deliberately broken template in production.
func withScaffoldFixture(t *testing.T, files []scaffoldFile, tfs fstest.MapFS) {
	t.Helper()
	origFiles, origFS := scaffoldFiles, scaffoldTemplatesFS
	scaffoldFiles = files
	scaffoldTemplatesFS = tfs
	t.Cleanup(func() {
		scaffoldFiles = origFiles
		scaffoldTemplatesFS = origFS
	})
}

// TestRenderTemplate_ParseError covers renderTemplate's template.Parse error
// branch via a syntactically invalid template body.
func TestRenderTemplate_ParseError(t *testing.T) {
	withScaffoldFixture(t, scaffoldFiles, fstest.MapFS{
		"bad-parse.tmpl": {Data: []byte("{{ .Module ")}, // unterminated action
	})
	if _, err := renderTemplate("bad-parse.tmpl", scaffoldData{Module: "x"}); err == nil {
		t.Fatal("expected a template.Parse error for an unterminated action")
	} else if !strings.Contains(err.Error(), "parse template") {
		t.Errorf("error = %q, want the parse-template prefix", err.Error())
	}
}

// TestRenderTemplate_ExecuteError covers renderTemplate's tmpl.Execute error
// branch: scaffoldData is a struct, so referencing a field it does not have
// fails at execute time regardless of the missingkey option (that option
// only governs map-key lookups).
func TestRenderTemplate_ExecuteError(t *testing.T) {
	withScaffoldFixture(t, scaffoldFiles, fstest.MapFS{
		"bad-execute.tmpl": {Data: []byte("{{ .NoSuchField }}")},
	})
	if _, err := renderTemplate("bad-execute.tmpl", scaffoldData{Module: "x"}); err == nil {
		t.Fatal("expected a tmpl.Execute error for a nonexistent struct field")
	} else if !strings.Contains(err.Error(), "render template") {
		t.Errorf("error = %q, want the render-template prefix", err.Error())
	}
}

// TestScaffold_RenderTemplateErrorPropagates covers scaffold()'s own
// renderTemplate-error branch (the loop's early return), and — driven
// through runNew via run(["new", ...]) — runNew's "scaffold failed" branch
// in the same call.
func TestScaffold_RenderTemplateErrorPropagates(t *testing.T) {
	withScaffoldFixture(t, []scaffoldFile{
		{Template: "bad.tmpl", Output: "bad.txt"},
	}, fstest.MapFS{
		"bad.tmpl": {Data: []byte("{{ .Module ")}, // unterminated -> parse error
	})

	dir := filepath.Join(t.TempDir(), "newbot")
	var out, errb bytes.Buffer
	code := run([]string{"new", dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("new with a broken template: code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "aisotropy new:") {
		t.Fatalf("stderr = %q, want the aisotropy-new error prefix", errb.String())
	}
}

// TestScaffold_MkdirAllDestSubdirFails covers scaffold()'s
// os.MkdirAll(filepath.Dir(dest), ...) error branch: the Output path names a
// subdirectory that already exists as a plain FILE, so MkdirAll cannot
// create it.
func TestScaffold_MkdirAllDestSubdirFails(t *testing.T) {
	withScaffoldFixture(t, []scaffoldFile{
		{Template: "ok.tmpl", Output: "collide/x.txt"},
	}, fstest.MapFS{
		"ok.tmpl": {Data: []byte("{{ .Module }}")},
	})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "collide"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := scaffold(dir, scaffoldData{Module: "m"}); err == nil {
		t.Fatal("expected an error when Output's subdirectory collides with an existing file")
	} else if !strings.Contains(err.Error(), "create") {
		t.Errorf("error = %q, want the create-directory prefix", err.Error())
	}
}

// TestScaffold_WriteFileDestIsDirectoryFails covers scaffold()'s
// os.WriteFile error branch: the destination file path already exists as a
// directory.
func TestScaffold_WriteFileDestIsDirectoryFails(t *testing.T) {
	withScaffoldFixture(t, []scaffoldFile{
		{Template: "ok.tmpl", Output: "x.txt"},
	}, fstest.MapFS{
		"ok.tmpl": {Data: []byte("{{ .Module }}")},
	})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := scaffold(dir, scaffoldData{Module: "m"}); err == nil {
		t.Fatal("expected an error when the destination file path is an existing directory")
	} else if !strings.Contains(err.Error(), "write") {
		t.Errorf("error = %q, want the write-file prefix", err.Error())
	}
}

// TestScaffold_TopLevelMkdirAllFails covers scaffold()'s OWN top-level
// os.MkdirAll(dir, ...) error branch (distinct from ensureEmptyTarget's
// separate, earlier os.Stat-based check): dir's parent directory is not
// writable, so MkdirAll cannot create dir itself even though dir does not
// yet exist (ensureEmptyTarget would report no error for a not-yet-existing
// path). Skipped when running as root.
func TestScaffold_TopLevelMkdirAllFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix permission checks")
	}

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod parent read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	dir := filepath.Join(parent, "newbot")
	if _, err := scaffold(dir, scaffoldData{Module: "m"}); err == nil {
		t.Fatal("expected an error creating dir under a non-writable parent")
	} else if !strings.Contains(err.Error(), "create") {
		t.Errorf("error = %q, want the create-directory prefix", err.Error())
	}
}

// TestEnsureEmptyTarget_StatErrorNotIsNotExist covers ensureEmptyTarget's
// "os.Stat failed for a reason OTHER than not-exist" branch — distinct from
// TestNewRefusesFileTarget (where dir itself IS a file, so os.Stat succeeds
// and info.IsDir() is false). Here dir is a path segment BELOW an existing
// plain file, so os.Stat(dir) itself fails with ENOTDIR.
func TestEnsureEmptyTarget_StatErrorNotIsNotExist(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "subdir") // a path segment (blocker) is a file, not a dir

	if err := ensureEmptyTarget(dir); err == nil {
		t.Fatal("expected a stat error when a parent path segment is a plain file")
	} else if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error = %q, want the stat-error prefix", err.Error())
	}

	var out, errb bytes.Buffer
	code := run([]string{"new", dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("new onto a path under a file: code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
}
