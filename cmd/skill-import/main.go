// Command skill-import is the PoC for ADR-0022's Agent-Skills import surface.
//
// It reads an Agent Skill folder (agentskills.io: a SKILL.md with YAML
// frontmatter + optional scripts/, references/, assets/) and PRINTS the exact
// aisotropy wiring the skill maps to — typed tools / MCP servers for scripts,
// literal personas for instructions, ingested knowledge for references. It
// writes nothing and injects nothing: the whole point of ADR-0022 is that a
// skill's behavior becomes explicit aisotropy primitives the operator can read,
// never hidden runtime prompt-injection (the anti-magic gate, ADR-0002).
//
// Usage:
//
//	go run ./cmd/skill-import examples/skills/pdf-form-filler
//
// The output is a human-readable mapping report plus a compile-ready Go wiring
// snippet against the public github.com/Luum-Home/aisotropy/aisotropy package.
// It is a code generator's *preview*, deliberately not an auto-applier: an
// operator reads the generated persona string and the scope gates before
// standing the bot up. That review step is the anti-magic tax ADR-0022 accepts.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

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
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: skill-import <skill-dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "skill-import: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	fm, body, err := parseSkill(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return err
	}
	resolveDefaults(&fm)

	var b strings.Builder
	writeReport(&b, dir, fm, body)
	fmt.Print(b.String())
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

func writeReport(b *strings.Builder, dir string, fm frontMatter, body string) {
	m := fm.Metadata.Aisotropy
	line(b, "==============================================================")
	line(b, "  Agent Skill -> aisotropy mapping (ADR-0022, PoC preview)")
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
		line(b, "    The importer will not emit an ungated executable tool — assign a")
		line(b, "    required-scope in metadata.aisotropy before wiring.")
	}
	if injectsSilently(body) {
		line(b, "  ! NOTE: instructions imply auto-load/always-on activation. That hint is")
		line(b, "    NOT honored as silent injection; it is mapped to explicit product_id")
		line(b, "    selection. If the skill only works via unseen prose, it is declined.")
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
			line(b, "\taisotropy.WithTypedTool[%s](%q, %q, %q, %sHandler),",
				structName(s.ToolName), s.ToolName, oneLine(fm.Description), m.RequiredScope, lowerFirst(structName(s.ToolName)))
		} else {
			line(b, "\taisotropy.WithMCPServer(%q, %q, %q),", m.RequiredScope, orDefault(s.Interpreter, "<interpreter>"), filepath.Join(dir, s.Path))
		}
	}
	line(b, ")")
	line(b, "")
	// Knowledge ingestion is a runtime call (needs ctx + tenant), not a builder
	// option — emit it as the post-New step it is.
	for _, r := range m.References {
		abs := filepath.Join(dir, r.Path)
		line(b, "// references/%s -> ingested knowledge (ADR-0018):", filepath.Base(r.Path))
		line(b, "// content, _ := os.ReadFile(%q)", abs)
		line(b, "// app.IngestTenantKnowledge(ctx, tenantID, aisotropy.Document{ID: %q, Content: string(content), Source: %q})", r.DocID, r.Path)
	}
	for _, s := range m.Scripts {
		if s.MapTo == "typed-tool" {
			line(b, "//")
			line(b, "// NOTE: %sHandler is the operator-written bridge that shells out to", lowerFirst(structName(s.ToolName)))
			line(b, "// %s (interpreter %q) with the decoded args. The importer does NOT", s.Path, orDefault(s.Interpreter, "<interpreter>"))
			line(b, "// generate execution glue — running third-party code is an explicit,")
			line(b, "// reviewed step (supply-chain gate).")
			break
		}
	}
}

// injectsSilently is a heuristic flag for instructions that presume ambient,
// always-on activation — the pattern ADR-0022 refuses to honor as magic.
func injectsSilently(body string) bool {
	low := strings.ToLower(body)
	for _, s := range []string{"always load", "auto-load", "automatically inject", "always inject", "on every request"} {
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

// --- small formatting helpers -------------------------------------------------

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
	parts := strings.FieldsFunc(tool, func(r rune) bool { return r == '_' || r == '-' })
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(strings.Title(p)) //nolint:staticcheck // ASCII identifiers only; strings.Title is adequate here
	}
	sb.WriteString("Args")
	return sb.String()
}

func goFieldName(arg string) string {
	parts := strings.FieldsFunc(arg, func(r rune) bool { return r == '_' || r == '-' })
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(strings.Title(p)) //nolint:staticcheck // ASCII identifiers only
	}
	return sb.String()
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

// goRawString renders body as a Go string literal for the generated snippet,
// preferring a raw backtick literal (readable), falling back to a quoted literal
// when the body itself contains a backtick.
func goRawString(body string) string {
	if !strings.Contains(body, "`") {
		return "`" + body + "`"
	}
	return strconv.Quote(body)
}
