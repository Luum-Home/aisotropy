package main

// The `new` subcommand scaffolds a NEW, already-running Aisotropy bot into a
// fresh directory:
//
//	aisotropy new <dir> [--module <module-path>]
//
// The motivating evidence: reaching a running app from a blank main.go took the
// first external adopter several hundred lines of boilerplate — App construction,
// a persona, a tool, auth wiring, a listen loop. This cuts that to one command.
// What it writes is a COMPLETE, compilable shape (not a stub): a persona, one
// typed example tool, the built-in dev chat, keyless auth, and App.Run — so a
// developer's very next steps are `go mod tidy` and `go run .`, landing them on a
// working browser chat at http://localhost:8080/ui.
//
// Deliberate reuse, no framework: the generated files are vanilla text/template
// templates embedded at build time (//go:embed) and rendered with a tiny data
// struct. Stdlib only (flag, embed, text/template), matching the `plugins` and
// `task` subcommands' dependency posture. The command surface grows by adding a
// template + a manifest row, not a dependency.
//
// HONEST SCOPE: `new` writes source files only. It does NOT run `go mod tidy`,
// fetch the aisotropy module, or compile anything — deliberately, so the command
// needs no network and no toolchain assumptions. The generated go.mod declares no
// `require` block; the first `go mod tidy` the developer runs discovers the
// import and pins it. The two development conveniences it wires (WithDevUI and
// NoAuth) are labeled not-for-production in the generated main.go and README.

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// templatesFS holds the scaffold templates, embedded at build time so the binary
// is self-contained (no external template directory to locate at runtime). The
// `all:` prefix ensures files are embedded regardless of name.
//
//go:embed all:templates
var templatesFS embed.FS

// scaffoldFile pairs an embedded template with the file it renders to inside the
// generated project. The output name is separate from the template name so a
// template can render to a dotfile (gitignore.tmpl -> .gitignore) that //go:embed
// could not itself embed (it skips names beginning with "." or "_").
type scaffoldFile struct {
	Template string // path within templatesFS
	Output   string // path within the generated project directory
}

// scaffoldFiles is the manifest of what a `new` project contains. Adding a file
// to the generated skeleton is a row here plus a template — no code change.
var scaffoldFiles = []scaffoldFile{
	{Template: "templates/main.go.tmpl", Output: "main.go"},
	{Template: "templates/go.mod.tmpl", Output: "go.mod"},
	{Template: "templates/README.md.tmpl", Output: "README.md"},
	{Template: "templates/gitignore.tmpl", Output: ".gitignore"},
}

// scaffoldData is the template context. It is intentionally tiny: a starter
// project needs only its module path, from which the display name is derived.
type scaffoldData struct {
	// Module is the Go module path written into go.mod and referenced in the
	// generated comments (e.g. "mybot" or "github.com/me/mybot").
	Module string
}

// newUsage prints the `new` subcommand summary.
func newUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  aisotropy new <dir> [--module <module-path>]

Scaffolds a new, runnable Aisotropy bot into <dir>: a persona, one example tool,
the built-in dev chat, keyless auth, and a main() that serves on :8080. The
directory must not already exist (or must be empty).

Next steps printed on success are:  cd <dir> && go mod tidy && go run .
then open http://localhost:8080/ui.

Flags:
  --module string   Go module path for go.mod (default: the directory's base name)
`)
}

// runNew dispatches the `new` command: it parses flags, validates the target,
// and writes the scaffold. Structured like runTask's parse/exec split so the
// argument contract is testable, but the write step is simple enough to inline.
func runNew(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		newUsage(stderr)
		return 2
	case args[0] == "-h", args[0] == "--help", args[0] == "help":
		newUsage(stdout)
		return 0
	}

	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	moduleFlag := fs.String("module", "", "Go module path for go.mod (default: the directory's base name)")
	// Permit flags on either side of the positional <dir>, matching `task run`.
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return 2 // flag package already printed the error to stderr
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "aisotropy new: missing <dir> argument")
		newUsage(stderr)
		return 2
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "aisotropy new: expected exactly one <dir>, got %d: %v\n", len(rest), rest)
		return 2
	}
	dir := rest[0]

	module, err := resolveModule(dir, *moduleFlag)
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy new: %v\n", err)
		return 2
	}

	if err := ensureEmptyTarget(dir); err != nil {
		fmt.Fprintf(stderr, "aisotropy new: %v\n", err)
		return 1
	}

	written, err := scaffold(dir, scaffoldData{Module: module})
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy new: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "scaffolded %s (module %s):\n", dir, module)
	for _, f := range written {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
	fmt.Fprintf(stdout, "\nnext steps:\n  cd %s\n  go mod tidy\n  go run .\nthen open http://localhost:8080/ui\n", dir)
	return 0
}

// resolveModule picks the module path: an explicit --module wins; otherwise it is
// derived from the target directory's base name (sanitized to a valid module
// token). It errors rather than silently substituting a placeholder when nothing
// usable can be derived, so the generated go.mod is never nonsense.
func resolveModule(dir, moduleFlag string) (string, error) {
	if m := strings.TrimSpace(moduleFlag); m != "" {
		if !isValidModulePath(m) {
			return "", fmt.Errorf("invalid --module %q: must be a non-empty path of dot/slash-separated tokens", m)
		}
		return m, nil
	}
	base := filepath.Base(filepath.Clean(dir))
	base = sanitizeModuleToken(base)
	if base == "" {
		return "", fmt.Errorf("cannot derive a module name from %q; pass --module <module-path>", dir)
	}
	return base, nil
}

// ensureEmptyTarget guarantees the destination is safe to write into: it must
// either not exist, or exist as an empty directory. Refusing a non-empty target
// prevents `new` from clobbering an existing project.
func ensureEmptyTarget(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // will be created by scaffold
		}
		return fmt.Errorf("stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("target %q exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %q: %w", dir, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("target directory %q is not empty", dir)
	}
	return nil
}

// scaffold renders every manifest entry into dir and returns the relative paths
// written, in manifest order. It creates the directory tree as needed. A render
// or write failure aborts immediately; because ensureEmptyTarget verified the
// destination was empty, a partial failure leaves only files this call created.
func scaffold(dir string, data scaffoldData) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %q: %w", dir, err)
	}
	var written []string
	for _, f := range scaffoldFiles {
		rendered, err := renderTemplate(f.Template, data)
		if err != nil {
			return written, err
		}
		dest := filepath.Join(dir, f.Output)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return written, fmt.Errorf("create %q: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, rendered, 0o644); err != nil {
			return written, fmt.Errorf("write %q: %w", dest, err)
		}
		written = append(written, f.Output)
	}
	return written, nil
}

// renderTemplate parses one embedded template and executes it against data. Parse
// errors carry the template path so a malformed template is easy to locate. The
// "missingkey=error" option turns a typo'd field reference into a hard error
// rather than a silently rendered "<no value>".
func renderTemplate(name string, data scaffoldData) ([]byte, error) {
	raw, err := fs.ReadFile(templatesFS, name)
	if err != nil {
		return nil, fmt.Errorf("read embedded template %q: %w", name, err)
	}
	tmpl, err := template.New(filepath.Base(name)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse template %q: %w", name, err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("render template %q: %w", name, err)
	}
	return []byte(b.String()), nil
}

// isValidModulePath is a lightweight check that a --module value is plausibly a
// module path: one or more non-empty tokens separated by "/", each token a
// non-empty sanitized token. It is deliberately permissive (Go's own module-path
// rules are stricter) — the goal is to reject obviously broken input like "" or
// "/" early, not to fully validate against the module spec.
func isValidModulePath(m string) bool {
	if strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
		return false
	}
	for _, seg := range strings.Split(m, "/") {
		if sanitizeModuleToken(seg) == "" {
			return false
		}
	}
	return true
}

// sanitizeModuleToken folds a single path segment to a safe module token: it
// keeps ASCII letters, digits, and the module-safe punctuation (".", "-", "_"),
// lowercases letters, maps whitespace to "-", and drops anything else. Leading
// and trailing separators are trimmed. Used both to derive a module name from a
// directory base and to validate --module segments.
func sanitizeModuleToken(seg string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(seg) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '\t':
			b.WriteRune('-')
		default:
			// drop
		}
	}
	return strings.Trim(b.String(), "-._")
}
