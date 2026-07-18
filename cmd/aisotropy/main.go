// Command aisotropy is the operator-facing companion CLI to the aisotropyd
// server. Where aisotropyd BOOTS the runtime (and scans AISOTROPY_PLUGIN_DIR for
// plugin manifests at startup, Wave 2), this binary MANAGES what that directory
// contains — the install/inspect/uninstall half of the plugin lifecycle:
//
//	aisotropy plugins add <path-or-dir>   parse+validate a manifest, then install it
//	aisotropy plugins list                show installed manifests
//	aisotropy plugins remove <name>       uninstall by plugin name
//
// The registry it manages is a directory of `*plugin*.yaml` manifest files —
// exactly the shape aisotropyd's boot-time scan (internal/plugin.Load) reads. The
// directory is AISOTROPY_PLUGIN_DIR, or ~/.aisotropy/plugins when that is unset,
// overridable per-invocation with -dir. So a plugin `add`-ed here is picked up by
// the next aisotropyd boot with no further wiring.
//
// Deliberate reuse, no duplicated logic: ALL manifest parsing, license/field
// validation, and the manifest-filename convention come from internal/plugin
// (ParseManifest, and the IsManifestFile predicate that package exposes precisely
// "so callers can filter a listing"). This CLI adds only filesystem plumbing
// (copy in, list, delete) and presentation around those primitives — it never
// re-parses YAML, re-checks a license, or re-derives the glob itself.
//
// HONEST SCOPE (manifest-level registry): `add` installs the manifest FILE. It
// does not relocate sibling runtime assets a kind:mcp plugin may reference from
// run.args (e.g. a server.js next to the manifest). For such plugins keep the
// assets in place and point run.args at an absolute path, or install the manifest
// from its own directory and run aisotropyd with that as the working directory.
// This is stated plainly rather than pretending `add` packages a whole runtime.
//
// Stdlib only (flag) — no new dependency, consistent with the repo's dependency
// posture. The command surface is deliberately small; it grows by adding cases,
// not frameworks.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Luum-Home/aisotropy/internal/plugin"
)

// exitFunc is a seam over os.Exit: main() calls through it instead of
// os.Exit directly, so a test can substitute a non-terminating fake and
// observe the requested exit code without killing the test binary.
// Production always uses the real os.Exit.
var exitFunc = os.Exit

func main() {
	exitFunc(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entrypoint: it takes the argument list (os.Args[1:]) and
// explicit writers so tests can drive every command in-process and assert on the
// captured output, rather than shelling out to a built binary. The int return is
// the process exit code (0 ok, 1 runtime failure, 2 usage error) — the same
// convention flag.ContinueOnError and the stdlib tools use.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "plugins":
		return runPlugins(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		// Optional, build-tagged subcommands register here. The `task` subcommand
		// (sandboxed key-brokered coding, ADR-0036) needs a container runtime at
		// call time, so it is compiled only under -tags keybroker; the default
		// build owns no optional commands. runOptionalCommand returns handled=false
		// for anything it does not own, falling through to the unknown-command error.
		if code, handled := runOptionalCommand(args[0], args[1:], stdout, stderr); handled {
			return code
		}
		fmt.Fprintf(stderr, "aisotropy: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

// usage prints the top-level command summary.
func usage(w io.Writer) {
	fmt.Fprint(w, `aisotropy — operator CLI for the Aisotropy runtime

Usage:
  aisotropy new <dir>                   Scaffold a new, runnable bot (persona + tool + dev chat)
  aisotropy plugins add <path-or-dir>   Install a plugin manifest (parse+validate, then copy in)
  aisotropy plugins list                List installed plugin manifests
  aisotropy plugins remove <name>       Uninstall a plugin by name
`)
	// Build-tagged subcommands (e.g. `task` under -tags keybroker) append their
	// own usage line here so the default binary does not advertise a command it
	// cannot run.
	optionalUsage(w)
	fmt.Fprint(w, `
Plugin directory:
  -dir overrides it per command. Otherwise $AISOTROPY_PLUGIN_DIR is used, or
  ~/.aisotropy/plugins when that is unset. This is the same directory aisotropyd
  scans at boot, so an installed plugin is live on the next server start.
`)
}

// pluginsUsage prints the `plugins` subcommand summary.
func pluginsUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  aisotropy plugins add <path-or-dir>   Install a plugin manifest (parse+validate, then copy in)
  aisotropy plugins list                List installed plugin manifests
  aisotropy plugins remove <name>       Uninstall a plugin by name

Flags (all subcommands):
  -dir string   Plugin directory (default $AISOTROPY_PLUGIN_DIR or ~/.aisotropy/plugins)
`)
}

// runPlugins dispatches the `plugins` subcommands. Each parses its own -dir flag
// so the plugin directory can be overridden per invocation (and pointed at a temp
// dir under test) without an environment variable.
func runPlugins(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		pluginsUsage(stderr)
		return 2
	}
	sub := args[0]
	switch sub {
	case "add", "list", "remove":
		// handled below
	case "-h", "--help", "help":
		pluginsUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "aisotropy plugins: unknown subcommand %q\n\n", sub)
		pluginsUsage(stderr)
		return 2
	}

	// ContinueOnError so a bad flag returns a usage exit code instead of calling
	// os.Exit from inside the flag package (which would break the testable run()).
	fs := flag.NewFlagSet("plugins "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dirFlag := fs.String("dir", "", "plugin directory (default $AISOTROPY_PLUGIN_DIR or ~/.aisotropy/plugins)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2 // flag package already printed the error to stderr
	}
	dir, err := resolvePluginDir(*dirFlag)
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy plugins %s: %v\n", sub, err)
		return 1
	}

	switch sub {
	case "add":
		return pluginsAdd(dir, fs.Args(), stdout, stderr)
	case "list":
		return pluginsList(dir, fs.Args(), stdout, stderr)
	case "remove":
		return pluginsRemove(dir, fs.Args(), stdout, stderr)
	}
	return 2 // unreachable: sub was validated above
}

// resolvePluginDir picks the plugin directory: an explicit -dir wins; otherwise
// $AISOTROPY_PLUGIN_DIR (the exact variable aisotropyd reads at boot); otherwise
// ~/.aisotropy/plugins. It only RESOLVES the path — it does not create it, so a
// `list` against a not-yet-existing directory reports "no plugins" rather than
// materializing an empty directory as a side effect.
func resolvePluginDir(flagValue string) (string, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv("AISOTROPY_PLUGIN_DIR")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory (set -dir or AISOTROPY_PLUGIN_DIR): %w", err)
	}
	return filepath.Join(home, ".aisotropy", "plugins"), nil
}

// installed pairs a validated manifest with the file it was parsed from — the
// path the registry primitives (ParseManifest) do not themselves carry, and which
// `remove` needs in order to delete the right file.
type installed struct {
	Path     string
	Manifest plugin.Manifest
}

// scanManifests lists the manifest files already in dir and parses each through
// internal/plugin. It reuses plugin.IsManifestFile for the naming convention and
// plugin.ParseManifest for parse+validation — no glob or license logic is
// re-implemented here. It is tolerant on purpose (unlike plugin.Load, which is
// all-or-nothing): a single malformed manifest emits a warning via warn and is
// skipped, so it cannot block listing or removing the healthy ones. A missing
// directory is not an error — it yields an empty result.
func scanManifests(dir string, warn func(string)) ([]installed, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugin directory %q: %w", dir, err)
	}
	var out []installed
	for _, e := range entries {
		if e.IsDir() || !plugin.IsManifestFile(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		m, perr := plugin.ParseManifest(p)
		if perr != nil {
			if warn != nil {
				warn(fmt.Sprintf("skipping %s: %v", e.Name(), perr))
			}
			continue
		}
		out = append(out, installed{Path: p, Manifest: m})
	}
	// Deterministic order by plugin name for stable listings and test assertions.
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out, nil
}

// pluginsAdd installs one manifest (or every manifest in a directory) into dir.
// The source is parsed+validated through internal/plugin BEFORE any file is
// written, so an invalid manifest never lands in the registry. The installed file
// is named "<name>-plugin.yaml" (name sanitized) so it matches the manifest glob
// aisotropyd scans and is uniquely keyed by plugin name — re-adding a plugin
// updates it in place.
func pluginsAdd(dir string, posArgs []string, stdout, stderr io.Writer) int {
	if len(posArgs) != 1 {
		fmt.Fprintln(stderr, "aisotropy plugins add: expected exactly one <path-or-dir>")
		return 2
	}
	src := posArgs[0]

	sources, err := collectSources(src, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy plugins add: %v\n", err)
		return 1
	}
	if len(sources) == 0 {
		fmt.Fprintf(stderr, "aisotropy plugins add: no valid plugin manifest found at %q\n", src)
		return 1
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "aisotropy plugins add: create plugin directory %q: %v\n", dir, err)
		return 1
	}

	installedCount := 0
	for _, s := range sources {
		destName := manifestFileName(s.Manifest.Name)
		dest := filepath.Join(dir, destName)
		if err := copyFile(s.Path, dest); err != nil {
			fmt.Fprintf(stderr, "aisotropy plugins add: install %q: %v\n", s.Manifest.Name, err)
			return 1
		}
		fmt.Fprintf(stdout, "installed %s v%s (%s) -> %s\n",
			s.Manifest.Name, s.Manifest.Version, s.Manifest.Kind, dest)
		installedCount++
	}
	if installedCount > 1 {
		fmt.Fprintf(stdout, "%d plugins installed into %s\n", installedCount, dir)
	}
	return 0
}

// collectSources turns the add argument into the set of (path, manifest) pairs to
// install. A file is parsed directly; a directory is scanned with the same
// tolerant scan `list` uses, so `add <dir>` installs every valid manifest it
// finds. Either way the manifests are already validated by internal/plugin.
func collectSources(src string, stderr io.Writer) ([]installed, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", src, err)
	}
	if info.IsDir() {
		return scanManifests(src, func(msg string) {
			fmt.Fprintf(stderr, "aisotropy plugins add: %s\n", msg)
		})
	}
	m, err := plugin.ParseManifest(src)
	if err != nil {
		return nil, err // ParseManifest already wraps the path + the validation reason
	}
	return []installed{{Path: src, Manifest: m}}, nil
}

// pluginsList prints the installed manifests as an aligned table. An empty
// registry (or a not-yet-created directory) prints a friendly notice, not an
// error — nothing installed is a valid state.
func pluginsList(dir string, posArgs []string, stdout, stderr io.Writer) int {
	if len(posArgs) != 0 {
		fmt.Fprintln(stderr, "aisotropy plugins list: takes no arguments")
		return 2
	}
	items, err := scanManifests(dir, func(msg string) {
		fmt.Fprintf(stderr, "aisotropy plugins list: %s\n", msg)
	})
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy plugins list: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		fmt.Fprintf(stdout, "no plugins installed in %s\n", dir)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tKIND\tTRUST\tTOOLS")
	for _, it := range items {
		m := it.Manifest
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			m.Name, m.Version, m.Kind, trustLabel(m.Trust), len(m.Tools))
	}
	_ = tw.Flush()
	return 0
}

// pluginsRemove uninstalls the manifest whose plugin name matches the argument.
// It matches on the manifest's declared Name (not the filename), so removal works
// regardless of how the file was named on disk, and reports clearly when no such
// plugin is installed.
func pluginsRemove(dir string, posArgs []string, stdout, stderr io.Writer) int {
	if len(posArgs) != 1 {
		fmt.Fprintln(stderr, "aisotropy plugins remove: expected exactly one <name>")
		return 2
	}
	name := strings.TrimSpace(posArgs[0])
	if name == "" {
		fmt.Fprintln(stderr, "aisotropy plugins remove: name must not be empty")
		return 2
	}
	items, err := scanManifests(dir, func(msg string) {
		fmt.Fprintf(stderr, "aisotropy plugins remove: %s\n", msg)
	})
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy plugins remove: %v\n", err)
		return 1
	}
	for _, it := range items {
		if it.Manifest.Name == name {
			if err := os.Remove(it.Path); err != nil {
				fmt.Fprintf(stderr, "aisotropy plugins remove: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "removed %s (%s)\n", name, it.Path)
			return 0
		}
	}
	fmt.Fprintf(stderr, "aisotropy plugins remove: no installed plugin named %q in %s\n", name, dir)
	return 1
}

// trustLabel renders a manifest's Trust for the table: an unset trust (a valid,
// deployer-decides state) shows as "-" rather than an empty column.
func trustLabel(t plugin.Trust) string {
	if strings.TrimSpace(string(t)) == "" {
		return "-"
	}
	return string(t)
}

// manifestFileName derives the on-disk filename for an installed plugin from its
// name. The name is sanitized to a filesystem-safe token and suffixed with
// "-plugin.yaml" so the result (a) is uniquely keyed by plugin name, and (b)
// always satisfies plugin.IsManifestFile / the aisotropyd scan glob.
func manifestFileName(name string) string {
	return sanitizeName(name) + "-plugin.yaml"
}

// sanitizeName folds a plugin name to a safe filename token: path separators and
// whitespace become hyphens and the result is lowercased, so a name like
// "Acme Word Tools" installs as "acme-word-tools-plugin.yaml". A name that
// sanitizes to empty falls back to "plugin" so a file is still produced.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '\\' || r == '\t':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "plugin"
	}
	return out
}

// copyFile writes src's contents to dest (0o644), replacing dest if it exists.
// Installing a manifest is a copy, not a move: the source stays where it is so a
// user can re-install or keep it under version control.
func copyFile(src, dest string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %q: %w", src, err)
	}
	if err := os.WriteFile(dest, b, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", dest, err)
	}
	return nil
}
