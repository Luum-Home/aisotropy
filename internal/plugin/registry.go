package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry is an immutable, ordered set of validated plugin manifests discovered
// on disk. It is the discovery/governance surface: an operator (or the runtime at
// boot, Wave 2) scans a directory once, inspects List() to see what would be
// loaded, and hands individual manifests to LoadTools to actually launch them.
type Registry struct {
	manifests []Manifest
}

// List returns the discovered manifests in scan order (a defensive copy — the
// caller cannot mutate the registry's backing slice).
func (r *Registry) List() []Manifest {
	return append([]Manifest(nil), r.manifests...)
}

// manifestGlobs are the filename patterns Load treats as plugin manifests. The
// canonical name is `aisotropy-plugin.yaml`, but any `*plugin*.yaml` file is
// picked up so a directory can hold several (e.g. `foo-plugin.yaml`,
// `bar.plugin.yaml`).
var manifestGlobs = []string{"*plugin*.yaml", "*plugin*.yml"}

// Load scans dir (non-recursively) for plugin manifest files (`*plugin*.yaml` /
// `*plugin*.yml`), parses and validates each, and returns a Registry of the ones
// that pass. Files are processed in lexical order for determinism.
//
// It is strict: a manifest that fails to parse or validate makes Load return an
// error naming the offending file, rather than silently dropping it — a
// governance surface that quietly skipped a malformed (or copyleft-licensed)
// manifest would defeat its own purpose. A directory with no manifest files is
// not an error; it yields an empty Registry.
func Load(dir string) (*Registry, error) {
	seen := make(map[string]struct{})
	var paths []string
	for _, g := range manifestGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil {
			return nil, fmt.Errorf("plugin: scan %q: %w", dir, err)
		}
		for _, p := range matches {
			if _, ok := seen[p]; ok {
				continue // a file matching two globs is counted once
			}
			// Skip directories that happen to match the glob.
			info, statErr := os.Stat(p)
			if statErr != nil || info.IsDir() {
				continue
			}
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	manifests := make([]Manifest, 0, len(paths))
	for _, p := range paths {
		m, err := ParseManifest(p)
		if err != nil {
			return nil, err // ParseManifest already wraps the path
		}
		manifests = append(manifests, m)
	}
	return &Registry{manifests: manifests}, nil
}

// IsManifestFile reports whether base (a file's base name) matches the plugin
// manifest naming convention. Exposed so callers can filter a listing without
// re-deriving the globs.
func IsManifestFile(base string) bool {
	lower := strings.ToLower(base)
	if !strings.Contains(lower, "plugin") {
		return false
	}
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}
