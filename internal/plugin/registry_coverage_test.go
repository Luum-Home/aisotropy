package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_BadGlobPattern exercises Load's filepath.Glob error branch: a
// directory whose path itself is an invalid glob pattern (an unterminated
// character class) must fail with a wrapped error, not a panic.
func TestLoad_BadGlobPattern(t *testing.T) {
	_, err := Load("bad[pattern")
	if err == nil {
		t.Fatal("expected an error for a malformed glob pattern")
	}
}

// TestLoad_SkipsDirectoriesMatchingGlob exercises the "skip directories that
// happen to match the glob" branch: a directory literally named
// "sub-plugin.yaml" must be ignored, not treated as a manifest file.
func TestLoad_SkipsDirectoriesMatchingGlob(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub-plugin.yaml"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "aisotropy-plugin.yaml", validMCPManifest)

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("discovered %d manifests, want 1 (the directory must be skipped): %+v", len(got), got)
	}
	if got[0].Name != "filesystem" {
		t.Fatalf("manifest = %+v, want the aisotropy-plugin.yaml one", got[0])
	}
}

// TestLoad_DedupsOverlappingGlobMatches exercises the "already seen" dedup
// branch: with the real manifestGlobs (*.yaml vs *.yml) the same file can
// never match twice, so this test temporarily widens the pattern set to
// force an overlap and prove Load counts the file only once.
func TestLoad_DedupsOverlappingGlobMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "aisotropy-plugin.yaml", validMCPManifest)

	original := manifestGlobs
	manifestGlobs = []string{"*plugin*.yaml", "aisotropy-*.yaml"} // both match the same file
	defer func() { manifestGlobs = original }()

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("discovered %d manifests, want 1 (deduped): %+v", len(got), got)
	}
}

// TestLoad_ParseErrorPropagatesPath exercises Load's "ParseManifest already
// wraps the path" error branch directly (already indirectly covered by
// TestLoad_FailsOnInvalidManifest, but this pins the exact path substring).
func TestLoad_ParseErrorPropagatesPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "broken-plugin.yaml", "kind: not-a-real-kind\n")

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error for an invalid manifest")
	}
	if !contains2(err.Error(), "broken-plugin.yaml") {
		t.Fatalf("error %q does not name the offending file", err.Error())
	}
}

func contains2(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
