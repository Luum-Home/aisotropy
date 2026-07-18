// Command skill-import is the ADR-0022 Agent-Skills import surface.
//
// It reads an Agent Skill folder (agentskills.io: a SKILL.md with YAML
// frontmatter + optional scripts/, references/, assets/) and maps it onto
// aisotropy's public primitives — typed tools / MCP servers for scripts, a
// literal persona for instructions, ingested knowledge for references. Nothing a
// skill carries is silently injected at runtime: the whole point of ADR-0022 is
// that a skill's behavior becomes explicit aisotropy primitives the operator can
// read (the anti-magic gate, ADR-0002).
//
// Two modes:
//
//	# preview (writes nothing): print the mapping report + illustrative wiring
//	go run ./cmd/skill-import examples/skills/pdf-form-filler
//
//	# generate: WRITE a compilable Go consumer module for the skill
//	go run ./cmd/skill-import -out /tmp/pdfbot examples/skills/pdf-form-filler
//
// In generate mode the tool emits its own Go module (its own go.mod with a
// replace directive back to this repo, mirroring examples/weatherbot) that
// registers the literal persona, a scope-gated typed tool with a real subprocess
// exec bridge for each schema-declared script (or WithMCPServer wiring + a TODO
// for unschematized scripts), and a knowledge-loading helper for references/.
// The generated module is buildable (`go build ./...`) — the operator reviews the
// persona string, the scope gates, and the exec bridges before standing it up.
// That review step is the anti-magic tax ADR-0022 accepts.
//
// Refusals abort generation (they do not emit unsafe wiring): a script tool with
// an empty required scope (ungated third-party code), or a prose-only skill that
// presumes silent always-on injection with nothing else to register.
package main

import (
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// aisotropyPkg is the public façade a generated bot imports (and nothing else —
// a separate module cannot reach internal/, so a compiling bot proves the public
// surface is sufficient).
const aisotropyPkg = "github.com/Luum-Home/aisotropy/aisotropy"

// frontMatter is the subset of SKILL.md YAML frontmatter this importer reads.
// name/description are the standard Agent-Skills fields; metadata.aisotropy is
// the operator-visible import manifest (a free-form extension point the format
// already sanctions) that pins how each script maps and which scope gates it.
type frontMatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	License      string   `yaml:"license"`
	AllowedTools []string `yaml:"allowed-tools"`
	Metadata     struct {
		Aisotropy aisotropyManifest `yaml:"aisotropy"`
	} `yaml:"metadata"`
}

// aisotropyManifest is the ADR-0022 import manifest under metadata.aisotropy.
// Absent it, the importer applies safe defaults (see resolveDefaults): scripts
// become MCP servers with an empty scope the operator must fill, instructions
// become a persona keyed on the skill name.
type aisotropyManifest struct {
	PersonaProductID string       `yaml:"persona-product-id"`
	RequiredScope    string       `yaml:"required-scope"`
	Scripts          []scriptSpec `yaml:"scripts"`
	References       []refSpec    `yaml:"references"`
}

type scriptSpec struct {
	Path        string    `yaml:"path"`
	Interpreter string    `yaml:"interpreter"`
	MapTo       string    `yaml:"map-to"` // "typed-tool" | "mcp"
	ToolName    string    `yaml:"tool-name"`
	Args        []argSpec `yaml:"args"`
}

type argSpec struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"` // string | bool | int | number
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

type refSpec struct {
	Path  string `yaml:"path"`
	DocID string `yaml:"doc-id"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run implements the CLI: it parses args, dispatches to preview/generate mode,
// and returns the process exit code. Extracted from main so tests can drive the
// whole command with fake args and capture stdout/stderr, instead of exercising
// only the unreachable-from-tests os.Exit-wrapped entry point.
func run(args []string, stdout, stderr io.Writer) int {
	var outDir string
	// Minimal flag handling so `-out <dir> <skill>` and `<skill>` both work
	// without pulling the whole flag package's usage noise into a two-mode tool.
	var positional []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-out" || args[i] == "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "skill-import: -out requires a directory argument")
				return 2
			}
			outDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-out="):
			outDir = strings.TrimPrefix(args[i], "-out=")
		case strings.HasPrefix(args[i], "--out="):
			outDir = strings.TrimPrefix(args[i], "--out=")
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: skill-import [-out <dir>] <skill-dir>")
		return 2
	}

	skillDir := positional[0]
	if outDir == "" {
		if err := runPreview(stdout, skillDir); err != nil {
			fmt.Fprintf(stderr, "skill-import: %v\n", err)
			return 1
		}
		return 0
	}
	if err := runGenerate(stdout, skillDir, outDir); err != nil {
		fmt.Fprintf(stderr, "skill-import: %v\n", err)
		return 1
	}
	return 0
}

func runPreview(w io.Writer, dir string) error {
	fm, body, err := parseSkill(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return err
	}
	resolveDefaults(&fm)

	var b strings.Builder
	writeReport(&b, dir, fm, body)
	fmt.Fprint(w, b.String())
	return nil
}

// parseSkill reads SKILL.md, splitting the leading `---` YAML frontmatter from
// the markdown body. The body is returned verbatim: it becomes the literal
// persona BasePrompt, so we must not reflow or normalize it — the operator sees
// exactly these bytes.
func parseSkill(path string) (frontMatter, string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied skill dir, by design
	if err != nil {
		return frontMatter{}, "", fmt.Errorf("read %s: %w", path, err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "---") {
		return frontMatter{}, "", fmt.Errorf("%s: missing YAML frontmatter (must start with ---)", path)
	}
	// Split off the frontmatter block: everything between the first two `---`
	// fence lines.
	rest := strings.TrimPrefix(text, "---")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return frontMatter{}, "", fmt.Errorf("%s: unterminated YAML frontmatter", path)
	}
	fmText := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	// Drop the single newline the closing fence may leave, then trim trailing
	// whitespace only (leading structure of the instructions is significant).
	body = strings.TrimLeft(body, "\n")
	body = strings.TrimRight(body, "\n") + "\n"

	var fm frontMatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return frontMatter{}, "", fmt.Errorf("%s: parse frontmatter: %w", path, err)
	}
	if fm.Name == "" {
		return frontMatter{}, "", fmt.Errorf("%s: frontmatter 'name' is required", path)
	}
	return fm, strings.TrimSpace(body), nil
}

// resolveDefaults fills the aisotropy manifest with the safe fallbacks ADR-0022
// prescribes when a skill ships no explicit metadata.aisotropy block.
func resolveDefaults(fm *frontMatter) {
	m := &fm.Metadata.Aisotropy
	if m.PersonaProductID == "" {
		m.PersonaProductID = fm.Name
	}
	for i := range m.Scripts {
		if m.Scripts[i].MapTo == "" {
			m.Scripts[i].MapTo = "mcp" // opaque code -> MCP unless the skill declares an arg schema
		}
		if m.Scripts[i].ToolName == "" {
			m.Scripts[i].ToolName = toolNameFromPath(m.Scripts[i].Path)
		}
	}
}

// ---------------------------------------------------------------------------
// Preview mode (the original PoC: reads, prints, writes nothing).
// ---------------------------------------------------------------------------

func writeReport(b *strings.Builder, dir string, fm frontMatter, body string) {
	m := fm.Metadata.Aisotropy
	line(b, "==============================================================")
	line(b, "  Agent Skill -> aisotropy mapping (ADR-0022, preview)")
	line(b, "==============================================================")
	line(b, "")
	line(b, "skill:        %s", fm.Name)
	line(b, "description:  %s", oneLine(fm.Description))
	line(b, "license:      %s", orDash(fm.License))
	line(b, "source dir:   %s", dir)
	line(b, "")

	writeMappingTable(b, dir, fm)
	writeAntiMagic(b, fm, body)
	writeWiring(b, dir, fm, body)

	if strings.EqualFold(fm.License, "AGPL-3.0") || strings.Contains(strings.ToUpper(fm.License), "SSPL") {
		line(b, "")
		line(b, "LICENSE GATE (constitutional): %s is on the block list — an", fm.License)
		line(b, "imported skill's code + content is a dependency; do NOT adopt without an ADR.")
	}
	line(b, "")
	line(b, "Run with -out <dir> to WRITE a compilable Go module for this skill.")
	_ = m
}

func writeMappingTable(b *strings.Builder, dir string, fm frontMatter) {
	m := fm.Metadata.Aisotropy
	line(b, "-- mapping ---------------------------------------------------")
	line(b, "  SKILL.md name/description  ->  tool/persona registration metadata")
	line(b, "  SKILL.md instructions      ->  aisotropy.WithPersona(%q, Persona{BasePrompt: <literal>})", m.PersonaProductID)

	for _, s := range m.Scripts {
		switch s.MapTo {
		case "typed-tool":
			line(b, "  scripts/%-18s ->  WithTypedTool[%s](%q, ...) scope=%q", filepath.Base(s.Path), structName(s.ToolName), s.ToolName, m.RequiredScope)
		default:
			line(b, "  scripts/%-18s ->  WithMCPServer(%q, %q, %q)", filepath.Base(s.Path), m.RequiredScope, orDefault(s.Interpreter, "<interpreter>"), s.Path)
		}
	}
	for _, r := range m.References {
		line(b, "  %-26s ->  App.IngestTenantKnowledge (KindDocument, doc-id %q)", r.Path, r.DocID)
	}
	// Any references/ or assets/ file not explicitly declared is still
	// ingestible knowledge — surface it so nothing is silently dropped.
	for _, extra := range undeclaredContent(dir, m) {
		line(b, "  %-26s ->  (content) ingest as KindDocument [not in manifest]", extra)
	}
	line(b, "")
}

func writeAntiMagic(b *strings.Builder, fm frontMatter, body string) {
	m := fm.Metadata.Aisotropy
	line(b, "-- anti-magic decisions (ADR-0002 gate) ----------------------")
	line(b, "  * The SKILL.md instructions are surfaced as a LITERAL persona string")
	line(b, "    the operator registers and can read in full — NOT auto-injected at")
	line(b, "    runtime. Progressive-disclosure \"activation\" maps to operator-visible")
	line(b, "    product_id selection (product_id=%q), never silent injection.", m.PersonaProductID)
	line(b, "  * scripts/ (executable behavior) become scope-gated tools (scope=%q).", m.RequiredScope)
	line(b, "    The model is only offered them when the request's scopes include it.")
	line(b, "  * references/ (non-behavioral content) become ingested knowledge (RAG),")
	line(b, "    retrieved on relevance — not prompt behavior.")
	if m.RequiredScope == "" && len(m.Scripts) > 0 {
		line(b, "  ! REFUSAL/HOLD: a script maps to a tool with an EMPTY required scope.")
		line(b, "    -out generation ABORTS here — assign a required-scope in")
		line(b, "    metadata.aisotropy before wiring an executable tool.")
	}
	if injectsSilently(body) {
		line(b, "  ! NOTE: instructions imply auto-load/always-on activation. That hint is")
		line(b, "    NOT honored as silent injection; it is mapped to explicit product_id")
		line(b, "    selection. A prose-only skill that ONLY works this way is declined.")
	}
	line(b, "")
}

func writeWiring(b *strings.Builder, dir string, fm frontMatter, body string) {
	m := fm.Metadata.Aisotropy
	line(b, "-- generated wiring (public aisotropy surface) ---------------")
	line(b, "")

	// Typed-tool arg structs first, so the New(...) call below references them.
	for _, s := range m.Scripts {
		if s.MapTo != "typed-tool" {
			continue
		}
		line(b, "// %s: typed args derived from the skill's declared script inputs.", s.ToolName)
		line(b, "type %s struct {", structName(s.ToolName))
		for _, a := range s.Args {
			tag := fmt.Sprintf("`json:%q jsonschema:%q`", a.Name, jsonschemaTag(a))
			line(b, "\t%s %s %s", goFieldName(a.Name), goType(a.Type), tag)
		}
		line(b, "}")
		line(b, "")
	}

	line(b, "app := aisotropy.New(")
	line(b, "\taisotropy.WithPersona(%q, aisotropy.Persona{", m.PersonaProductID)
	line(b, "\t\tBasePrompt: %s,", goRawString(body))
	line(b, "\t}),")
	for _, s := range m.Scripts {
		if s.MapTo == "typed-tool" {
			line(b, "\taisotropy.WithTypedTool[%s](%q, %q, %q, %s),",
				structName(s.ToolName), s.ToolName, oneLine(fm.Description), m.RequiredScope, handlerName(s.ToolName))
		} else {
			line(b, "\taisotropy.WithMCPServer(%q, %q, %q),", m.RequiredScope, orDefault(s.Interpreter, "<interpreter>"), filepath.Join(dir, s.Path))
		}
	}
	line(b, ")")
	line(b, "")
	for _, r := range m.References {
		abs := filepath.Join(dir, r.Path)
		line(b, "// references/%s -> ingested knowledge (ADR-0018):", filepath.Base(r.Path))
		line(b, "// content, _ := os.ReadFile(%q)", abs)
		line(b, "// app.IngestTenantKnowledge(ctx, tenantID, aisotropy.Document{ID: %q, Content: string(content), Source: %q})", r.DocID, r.Path)
	}
}

// ---------------------------------------------------------------------------
// Generate mode (the upgrade: WRITE a compilable module).
// ---------------------------------------------------------------------------

// runGenerate writes a compilable Go consumer module for the skill under outDir,
// then runs `go mod tidy` so the module resolves its dependencies and builds.
func runGenerate(w io.Writer, skillDir, outDir string) error {
	fm, body, err := parseSkill(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return err
	}
	resolveDefaults(&fm)
	m := fm.Metadata.Aisotropy

	// Refusal gates — abort BEFORE writing any wiring (ADR-0022 anti-magic).
	if err := checkRefusals(fm, body, skillDir); err != nil {
		return err
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir %s: %w", outDir, err)
	}

	moduleName := "skillimport/" + sanitizeModuleSegment(fm.Name)

	// go.mod: minimal (require aisotropy + absolute replace). `go mod tidy` fills
	// in the rest, exactly as a real `aisotropy import-skill` would.
	goMod := fmt.Sprintf("module %s\n\ngo 1.25.1\n\nrequire %s v0.0.0\n\nreplace %s => %s\n",
		moduleName, moduleRoot(aisotropyPkg), moduleRoot(aisotropyPkg), repoRoot)
	if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte(goMod), 0o644); err != nil { //nolint:gosec // generated scaffold, operator-owned
		return fmt.Errorf("write go.mod: %w", err)
	}

	// Copy each script into the module so the exec bridge / MCP wiring points at a
	// self-contained, reviewable copy (the supply-chain artifact travels with the
	// scaffold).
	var written []string
	for _, s := range m.Scripts {
		if s.Path == "" {
			continue
		}
		dst := filepath.Join(outDir, "scripts", filepath.Base(s.Path))
		if err := copyFile(filepath.Join(skillDir, s.Path), dst); err != nil {
			return fmt.Errorf("copy script %s: %w", s.Path, err)
		}
		written = append(written, filepath.Join("scripts", filepath.Base(s.Path)))
	}

	// Copy declared references into knowledge/ for //go:embed.
	for _, r := range m.References {
		if r.Path == "" {
			continue
		}
		dst := filepath.Join(outDir, "knowledge", filepath.Base(r.Path))
		if err := copyFile(filepath.Join(skillDir, r.Path), dst); err != nil {
			return fmt.Errorf("copy reference %s: %w", r.Path, err)
		}
		written = append(written, filepath.Join("knowledge", filepath.Base(r.Path)))
	}

	// main.go
	mainSrc, err := generateMainGo(fm, body)
	if err != nil {
		return fmt.Errorf("generate main.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "main.go"), mainSrc, 0o644); err != nil { //nolint:gosec // generated scaffold, operator-owned
		return fmt.Errorf("write main.go: %w", err)
	}
	written = append(written, "main.go", "go.mod")

	// knowledge.go (only when the skill ships declared references).
	if len(m.References) > 0 {
		knowSrc, kerr := generateKnowledgeGo(m)
		if kerr != nil {
			return fmt.Errorf("generate knowledge.go: %w", kerr)
		}
		if err := os.WriteFile(filepath.Join(outDir, "knowledge.go"), knowSrc, 0o644); err != nil { //nolint:gosec // generated scaffold, operator-owned
			return fmt.Errorf("write knowledge.go: %w", err)
		}
		written = append(written, "knowledge.go")
	}

	// Resolve dependencies so the module builds.
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	if out, terr := tidy.CombinedOutput(); terr != nil {
		return fmt.Errorf("go mod tidy in %s failed: %w\n%s", outDir, terr, out)
	}
	written = append(written, "go.sum")

	printGenerateReport(w, outDir, fm, written)
	return nil
}

// checkRefusals enforces the ADR-0022 anti-magic gates that must ABORT
// generation rather than emit unsafe wiring.
func checkRefusals(fm frontMatter, body, skillDir string) error {
	m := fm.Metadata.Aisotropy

	// Gate 1: ungated executable behavior. A script tool with an empty required
	// scope is third-party code the model could reach unconditionally. Refuse.
	if strings.TrimSpace(m.RequiredScope) == "" && len(m.Scripts) > 0 {
		return fmt.Errorf("REFUSED (ADR-0022): %d script(s) map to executable tools but required-scope is empty — "+
			"skill-import will not emit an ungated tool that runs third-party code. "+
			"Set metadata.aisotropy.required-scope before generating", len(m.Scripts))
	}

	// Gate 2: inject-only prose. A skill with NOTHING to register but prose that
	// presumes silent always-on injection can only work as magic. Refuse.
	hasContent := len(m.Scripts) > 0 || len(m.References) > 0 || len(undeclaredContent(skillDir, m)) > 0
	if !hasContent && injectsSilently(body) {
		return fmt.Errorf("REFUSED (ADR-0022): the skill has no scripts and no references, and its instructions " +
			"presume silent always-on injection — it can only work by injecting prose the operator never sees. " +
			"Rewrite the instructions as an operator-owned persona (drop the 'always inject' framing) to import it")
	}
	return nil
}

// generateMainGo emits the gofmt'd main.go for the skill's consumer module.
func generateMainGo(fm frontMatter, body string) ([]byte, error) {
	m := fm.Metadata.Aisotropy

	hasTyped := false
	for _, s := range m.Scripts {
		if s.MapTo == "typed-tool" {
			hasTyped = true
			break
		}
	}

	var b strings.Builder
	line(&b, "// Code generated by skill-import from the %q Agent Skill (ADR-0022).", fm.Name)
	line(&b, "//")
	line(&b, "// This is an operator-owned scaffold, not a finished bot: review the literal")
	line(&b, "// persona below and every exec bridge (the supply-chain boundary — each runs")
	line(&b, "// third-party code) before enabling it. It imports ONLY the public aisotropy")
	line(&b, "// package; because this is a separate module, Go forbids reaching into internal/.")
	line(&b, "package main")
	line(&b, "")

	// Imports — computed precisely so the file compiles with no unused import.
	std := []string{"context", "log"}
	if hasTyped {
		std = append(std, "bytes", "encoding/json", "fmt", "os", "os/exec")
	}
	sort.Strings(std)
	line(&b, "import (")
	for _, s := range std {
		line(&b, "\t%q", s)
	}
	line(&b, "")
	line(&b, "\t%q", aisotropyPkg)
	line(&b, ")")
	line(&b, "")

	// Typed-tool arg structs.
	for _, s := range m.Scripts {
		if s.MapTo != "typed-tool" {
			continue
		}
		line(&b, "// %s is the typed argument contract for the %q tool, derived from the", structName(s.ToolName), s.ToolName)
		line(&b, "// skill's declared script inputs (struct-as-contract, ADR-0016 lever #1).")
		line(&b, "type %s struct {", structName(s.ToolName))
		for _, a := range s.Args {
			tag := fmt.Sprintf("`json:%q jsonschema:%q`", a.Name, jsonschemaTag(a))
			line(&b, "\t%s %s %s", goFieldName(a.Name), goType(a.Type), tag)
		}
		line(&b, "}")
		line(&b, "")
	}

	// Exec-bridge helper (only when there is at least one typed tool).
	if hasTyped {
		line(&b, "// scriptPath resolves the script an exec bridge shells out to: an env override,")
		line(&b, "// else the copy skill-import shipped alongside this module.")
		line(&b, "func scriptPath(env, def string) string {")
		line(&b, "\tif v := os.Getenv(env); v != \"\" {")
		line(&b, "\t\treturn v")
		line(&b, "\t}")
		line(&b, "\treturn def")
		line(&b, "}")
		line(&b, "")
	}

	// One exec-bridge handler per typed-tool script.
	for _, s := range m.Scripts {
		if s.MapTo != "typed-tool" {
			continue
		}
		interp := orDefault(s.Interpreter, "python3")
		envVar := "SKILL_" + strings.ToUpper(strings.ReplaceAll(s.ToolName, "-", "_")) + "_SCRIPT"
		defPath := filepath.ToSlash(filepath.Join("scripts", filepath.Base(s.Path)))
		line(&b, "// %s is the OPERATOR-OWNED exec bridge (the supply-chain boundary): it shells", handlerName(s.ToolName))
		line(&b, "// out to the skill's third-party script with the decoded, schema-validated args.")
		line(&b, "// skill-import generates the subprocess plumbing but NOT trust — vet and pin the")
		line(&b, "// script before enabling the %q scope.", m.RequiredScope)
		line(&b, "//")
		line(&b, "// Convention: args are marshaled to JSON on the script's stdin; stdout is the")
		line(&b, "// tool result (matches the sample's stdin->stdout JSON contract).")
		line(&b, "// TODO(operator): confirm this matches your script's actual I/O contract.")
		line(&b, "func %s(ctx context.Context, args %s) (string, error) {", handlerName(s.ToolName), structName(s.ToolName))
		line(&b, "\tscript := scriptPath(%q, %q)", envVar, defPath)
		line(&b, "\tpayload, err := json.Marshal(args)")
		line(&b, "\tif err != nil {")
		line(&b, "\t\treturn \"\", fmt.Errorf(%q, err)", s.ToolName+": marshal args: %w")
		line(&b, "\t}")
		line(&b, "\tcmd := exec.CommandContext(ctx, %q, script)", interp)
		line(&b, "\tcmd.Stdin = bytes.NewReader(payload)")
		line(&b, "\tvar stdout, stderr bytes.Buffer")
		line(&b, "\tcmd.Stdout = &stdout")
		line(&b, "\tcmd.Stderr = &stderr")
		line(&b, "\tif err := cmd.Run(); err != nil {")
		line(&b, "\t\treturn \"\", fmt.Errorf(%q, script, err, stderr.String())", s.ToolName+": exec %q: %w: %s")
		line(&b, "\t}")
		line(&b, "\treturn stdout.String(), nil")
		line(&b, "}")
		line(&b, "")
	}

	// main().
	line(&b, "func main() {")
	line(&b, "\tapp := aisotropy.New(")
	line(&b, "\t\taisotropy.WithPersona(%q, aisotropy.Persona{", m.PersonaProductID)
	line(&b, "\t\t\tBasePrompt: %s,", goRawString(body))
	line(&b, "\t\t}),")
	for _, s := range m.Scripts {
		if s.MapTo == "typed-tool" {
			line(&b, "\t\taisotropy.WithTypedTool[%s](", structName(s.ToolName))
			line(&b, "\t\t\t%q,", s.ToolName)
			line(&b, "\t\t\t%q,", oneLine(fm.Description))
			line(&b, "\t\t\t%q,", m.RequiredScope)
			line(&b, "\t\t\t%s,", handlerName(s.ToolName))
			line(&b, "\t\t),")
		} else {
			interp := orDefault(s.Interpreter, "python3")
			defPath := filepath.ToSlash(filepath.Join("scripts", filepath.Base(s.Path)))
			line(&b, "\t\t// %s has no declared arg schema -> MCP server (ADR-0016 lever #2).", filepath.Base(s.Path))
			line(&b, "\t\t// TODO(operator): supply an MCP server for this script. skill-import does NOT")
			line(&b, "\t\t// fabricate execution glue for unschematized code — wrap the script as a stdio")
			line(&b, "\t\t// MCP server (any language) and point WithMCPServer at it. The path below is the")
			line(&b, "\t\t// raw script, which is NOT an MCP server unless you make it one.")
			line(&b, "\t\taisotropy.WithMCPServer(%q, %q, %q),", m.RequiredScope, interp, defPath)
		}
	}
	line(&b, "\t)")
	line(&b, "")
	if len(m.References) > 0 {
		line(&b, "\t// references/ -> tenant-shared ingested knowledge (ADR-0018/0019). Operator-")
		line(&b, "\t// invoked: pick the tenant and the timing. IngestTenantKnowledge shares the same")
		line(&b, "\t// lazily-built semantic store Run uses (ensureSemantic memoizes it), so calling")
		line(&b, "\t// it before Run is safe. LoadTenantKnowledge is generated in knowledge.go.")
		line(&b, "\t//")
		line(&b, "\t//\tctx := context.Background()")
		line(&b, "\t//\tif _, err := LoadTenantKnowledge(ctx, app, \"your-tenant-id\"); err != nil {")
		line(&b, "\t//\t\tlog.Fatalf(\"load skill knowledge: %%v\", err)")
		line(&b, "\t//\t}")
		line(&b, "")
	}
	line(&b, "\tif err := app.Run(context.Background()); err != nil {")
	line(&b, "\t\tlog.Fatalf(%q, err)", sanitizeModuleSegment(fm.Name)+" bot exited: %v")
	line(&b, "\t}")
	line(&b, "}")

	return format.Source([]byte(b.String()))
}

// generateKnowledgeGo emits knowledge.go: an embedded FS of the skill's
// references and a LoadTenantKnowledge helper that ingests them (ADR-0018/0019).
func generateKnowledgeGo(m aisotropyManifest) ([]byte, error) {
	var b strings.Builder
	line(&b, "// Code generated by skill-import: the skill's references/ as ingested knowledge.")
	line(&b, "package main")
	line(&b, "")
	line(&b, "import (")
	line(&b, "\t\"context\"")
	line(&b, "\t\"embed\"")
	line(&b, "")
	line(&b, "\t%q", aisotropyPkg)
	line(&b, ")")
	line(&b, "")
	line(&b, "//go:embed knowledge")
	line(&b, "var knowledgeFS embed.FS")
	line(&b, "")
	line(&b, "// LoadTenantKnowledge ingests the skill's references/ as tenant-shared KindDocument")
	line(&b, "// knowledge (ADR-0018/0019), retrieved by RAG on relevance — never auto-injected")
	line(&b, "// into the prompt. Call it after New with the tenant you want the corpus shared")
	line(&b, "// across; it returns the number of chunks written.")
	line(&b, "func LoadTenantKnowledge(ctx context.Context, app *aisotropy.App, tenantID string) (int, error) {")
	line(&b, "\tdocs := []aisotropy.Document{")
	for _, r := range m.References {
		embedPath := filepath.ToSlash(filepath.Join("knowledge", filepath.Base(r.Path)))
		line(&b, "\t\tmustDoc(%q, %q, %q),", r.DocID, embedPath, r.Path)
	}
	line(&b, "\t}")
	line(&b, "\treturn app.IngestTenantKnowledge(ctx, tenantID, docs...)")
	line(&b, "}")
	line(&b, "")
	line(&b, "// mustDoc reads one embedded reference into an aisotropy.Document. A missing")
	line(&b, "// embed is a build-time-copied file gone missing at runtime — a bug, so panic.")
	line(&b, "func mustDoc(id, embedPath, source string) aisotropy.Document {")
	line(&b, "\tcontent, err := knowledgeFS.ReadFile(embedPath)")
	line(&b, "\tif err != nil {")
	line(&b, "\t\tpanic(\"skill knowledge missing from embed: \" + embedPath)")
	line(&b, "\t}")
	line(&b, "\treturn aisotropy.Document{ID: id, Content: string(content), Source: source}")
	line(&b, "}")

	return format.Source([]byte(b.String()))
}

func printGenerateReport(w io.Writer, outDir string, fm frontMatter, written []string) {
	m := fm.Metadata.Aisotropy
	sort.Strings(written)
	fmt.Fprintf(w, "skill-import: generated a compilable consumer module for %q\n\n", fm.Name)
	fmt.Fprintf(w, "  out dir:  %s\n", outDir)
	fmt.Fprintf(w, "  module:   skillimport/%s\n", sanitizeModuleSegment(fm.Name))
	fmt.Fprintf(w, "  persona:  WithPersona(%q, {BasePrompt: <literal SKILL.md instructions>})\n", m.PersonaProductID)
	typed, mcp := 0, 0
	for _, s := range m.Scripts {
		if s.MapTo == "typed-tool" {
			typed++
		} else {
			mcp++
		}
	}
	fmt.Fprintf(w, "  tools:    %d typed (scope=%q, exec bridge) + %d MCP (operator-supplied)\n", typed, m.RequiredScope, mcp)
	fmt.Fprintf(w, "  refs:     %d ingested-knowledge doc(s)\n\n", len(m.References))
	fmt.Fprintln(w, "  files:")
	for _, f := range written {
		fmt.Fprintf(w, "    %s\n", f)
	}
	fmt.Fprintf(w, "\n  verify:  (cd %s && go build ./...)\n", outDir)
	fmt.Fprintln(w, "  review:  the persona string + every exec bridge before running (supply-chain gate).")
}

// ---------------------------------------------------------------------------
// Heuristics + helpers.
// ---------------------------------------------------------------------------

// injectsSilently is a heuristic flag for instructions that presume ambient,
// always-on activation — the pattern ADR-0022 refuses to honor as magic.
func injectsSilently(body string) bool {
	low := strings.ToLower(body)
	for _, s := range []string{"always load", "auto-load", "automatically inject", "always inject", "on every request", "load on every request"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

// undeclaredContent lists references/ and assets/ files not named in the
// manifest, so a preview never silently omits ingestible content.
func undeclaredContent(dir string, m aisotropyManifest) []string {
	declared := map[string]bool{}
	for _, r := range m.References {
		declared[filepath.Clean(r.Path)] = true
	}
	var out []string
	for _, sub := range []string{"references", "assets"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			rel := filepath.Join(sub, e.Name())
			if !declared[filepath.Clean(rel)] {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// findRepoRoot walks up from the working directory to the module root that
// declares the aisotropy module, returning its absolute path for the generated
// module's replace directive.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	want := "module " + moduleRoot(aisotropyPkg)
	for {
		gomod := filepath.Join(dir, "go.mod")
		if raw, rerr := os.ReadFile(gomod); rerr == nil && strings.Contains(string(raw), want) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate the aisotropy repo root (no go.mod declaring %q above %s)", moduleRoot(aisotropyPkg), mustGetwd())
		}
		dir = parent
	}
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// moduleRoot strips the package path down to the module path
// (github.com/Luum-Home/aisotropy/aisotropy -> github.com/Luum-Home/aisotropy).
func moduleRoot(pkg string) string {
	return strings.TrimSuffix(pkg, "/aisotropy")
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // operator-supplied skill dir, by design
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644) //nolint:gosec // generated scaffold, operator-owned
}

func sanitizeModuleSegment(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		out = "skill"
	}
	return out
}

func line(b *strings.Builder, format string, args ...any) {
	fmt.Fprintf(b, format+"\n", args...)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func toolNameFromPath(p string) string {
	base := filepath.Base(p)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.ReplaceAll(base, "-", "_")
}

// structName turns a tool_name into an exported Go type name (fill_pdf_form ->
// FillPdfFormArgs).
func structName(tool string) string {
	return exportedCamel(tool) + "Args"
}

// handlerName turns a tool_name into the unexported exec-bridge handler name
// (fill_pdf_form -> fillPdfFormHandler).
func handlerName(tool string) string {
	return lowerFirst(exportedCamel(tool)) + "Handler"
}

func exportedCamel(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(strings.Title(p)) //nolint:staticcheck // ASCII identifiers only; strings.Title is adequate here
	}
	return sb.String()
}

func goFieldName(arg string) string {
	return exportedCamel(arg)
}

func goType(t string) string {
	switch t {
	case "bool":
		return "bool"
	case "int":
		return "int"
	case "number":
		return "float64"
	default:
		return "string"
	}
}

func jsonschemaTag(a argSpec) string {
	parts := []string{"description=" + oneLine(a.Description)}
	if a.Required {
		parts = append(parts, "required")
	}
	return strings.Join(parts, ",")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// goRawString renders body as a Go string literal for the generated code,
// preferring a raw backtick literal (readable), falling back to a quoted literal
// when the body itself contains a backtick.
func goRawString(body string) string {
	if !strings.Contains(body, "`") {
		return "`" + body + "`"
	}
	return strconv.Quote(body)
}
