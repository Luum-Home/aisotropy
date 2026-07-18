package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures: each is a map of relative-path -> file content, written into a
// fresh t.TempDir() skill directory by newSkillDir.
// ---------------------------------------------------------------------------

// fullTypedSkill exercises: a typed-tool script with every declared arg type
// (string/bool/int/number/unknown-default), a declared reference, undeclared
// references/assets content (including a subdirectory to hit the directory-skip
// branch), and an MIT license (the "no gate" license branch).
func fullTypedSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: full-typed
description: A fully-featured skill for coverage.
license: MIT
allowed-tools:
  - do_thing
metadata:
  aisotropy:
    persona-product-id: full-typed-persona
    required-scope: exec:full
    scripts:
      - path: scripts/do_thing.py
        interpreter: python3
        map-to: typed-tool
        tool-name: do_thing
        args:
          - name: input_path
            type: string
            required: true
            description: Path to the input file.
          - name: verbose
            type: bool
            required: false
            description: Verbose output.
          - name: retries
            type: int
            required: false
            description: Retry count.
          - name: threshold
            type: number
            required: false
            description: Threshold value.
          - name: mystery
            type: something-else
            required: false
            description: Unknown type falls back to string.
    references:
      - path: references/notes.md
        doc-id: full-typed/notes
---

# Full typed skill

Do the thing carefully. Confirm inputs first.
`,
		"scripts/do_thing.py":          "print(\"hi\")\n",
		"references/notes.md":          "# Notes\n\nSome notes.\n",
		"references/undeclared.md":     "undeclared ref content\n",
		"references/subdir/ignored.md": "nested, must be skipped as a directory entry\n",
		"assets/extra.txt":             "extra asset\n",
	}
}

// mcpDefaultSkill exercises: default map-to ("mcp"), default tool-name (derived
// from the script path), default persona-product-id (falls back to fm.Name), and
// the orDefault interpreter fallback branch (second script has no interpreter).
func mcpDefaultSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: mcp-default
description: MCP default mapping skill.
metadata:
  aisotropy:
    required-scope: exec:mcp
    scripts:
      - path: scripts/run-thing.sh
        interpreter: bash
      - path: scripts/no-interp.sh
---

Runs a script via mcp by default.
`,
		"scripts/run-thing.sh": "#!/bin/sh\necho hi\n",
		"scripts/no-interp.sh": "#!/bin/sh\necho hi\n",
	}
}

// gate1RefusalSkill has a script but an empty required-scope: preview shows a
// REFUSAL/HOLD note, generate mode must abort (ADR-0022 gate 1).
func gate1RefusalSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: gate1-refusal
description: Missing scope skill.
metadata:
  aisotropy:
    scripts:
      - path: scripts/x.py
        map-to: mcp
---
Do something with no scope declared.
`,
		"scripts/x.py": "print(\"x\")\n",
	}
}

// gate2RefusalSkill has no scripts, no references, and no undeclared content,
// with instructions that presume silent always-on injection: generate mode must
// abort (ADR-0022 gate 2).
func gate2RefusalSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: gate2-refusal
description: Silent injection only, no content.
---
Always load this persona automatically. Apply it on every request without being asked.
`,
	}
}

// silentWithContentSkill also uses always-on phrasing, but declares a reference
// (hasContent=true), so generate mode must NOT refuse it — this pins the
// short-circuit boundary of checkRefusals' gate 2 condition. It also has zero
// scripts, exercising generateMainGo's hasTyped=false path (no exec-bridge
// helper, no bytes/encoding/json/fmt/os/os-exec imports).
func silentWithContentSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: silent-with-content
description: Silent phrasing but has a reference, must not be refused.
metadata:
  aisotropy:
    references:
      - path: references/info.md
        doc-id: silent-with-content/info
---
Always load this reference material automatically before responding on every request.
`,
		"references/info.md": "info content\n",
	}
}

// backtickBodySkill has a backtick in its instructions body, forcing goRawString
// to take the strconv.Quote fallback branch when generating main.go.
func backtickBodySkill() map[string]string {
	return map[string]string{
		"SKILL.md": "---\n" +
			"name: backtick-body\n" +
			"description: Body with an inline code span.\n" +
			"metadata:\n" +
			"  aisotropy:\n" +
			"    required-scope: exec:code\n" +
			"    scripts:\n" +
			"      - path: scripts/run.py\n" +
			"        map-to: typed-tool\n" +
			"        tool-name: run_code\n" +
			"        args:\n" +
			"          - name: snippet\n" +
			"            type: string\n" +
			"            required: true\n" +
			"            description: Code snippet to run.\n" +
			"---\n" +
			"Use `run_code()` to execute snippets. Never run untrusted code without review.\n",
		"scripts/run.py": "print(\"run\")\n",
	}
}

// agplLicenseSkill triggers the exact "AGPL-3.0" license-gate branch.
func agplLicenseSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: agpl-skill
description: Skill under a blocked license.
license: AGPL-3.0
---
Follow the documented procedure.
`,
	}
}

// sspLicenseSkill triggers the "contains SSPL" license-gate branch (a license
// string that is not an exact "AGPL-3.0" match but still contains "SSPL").
func sspLicenseSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: sspl-skill
description: Skill under an SSPL-flavored license.
license: Server Side Public License (SSPL)
---
Follow the documented procedure.
`,
	}
}

// missingScriptFileSkill declares a script whose file is not actually present on
// disk, forcing copyFile's "copy script" error branch during generate.
func missingScriptFileSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: missing-script-file
description: Script path does not exist on disk.
metadata:
  aisotropy:
    required-scope: exec:missing
    scripts:
      - path: scripts/does-not-exist.py
        map-to: typed-tool
        tool-name: ghost
        args:
          - name: x
            type: string
            required: true
            description: x
---
Ghost script.
`,
	}
}

// missingReferenceFileSkill declares a reference whose file is not actually
// present on disk, forcing copyFile's "copy reference" error branch.
func missingReferenceFileSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: missing-reference-file
description: Reference path does not exist on disk.
metadata:
  aisotropy:
    references:
      - path: references/ghost.md
        doc-id: missing-reference-file/ghost
---
No always-on phrasing here, but the declared (missing) reference makes hasContent true.
`,
	}
}

// emptyPathEntriesSkill mixes a real script/reference with an entry that
// declares an empty path, covering the "skip entries with no path" continue
// branches in runGenerate's copy loops.
func emptyPathEntriesSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: empty-path-entries
description: Some script/reference entries declare no path.
metadata:
  aisotropy:
    required-scope: exec:mixed
    scripts:
      - path: ""
        map-to: mcp
        tool-name: ghost_empty
      - path: scripts/real.sh
        interpreter: bash
    references:
      - path: ""
        doc-id: mixed/empty
      - path: references/real.md
        doc-id: mixed/real
---
Mixed real and pathless entries.
`,
		"scripts/real.sh":    "#!/bin/sh\necho real\n",
		"references/real.md": "real reference content\n",
	}
}

// invalidIdentifierSkill declares an arg name that is not a valid Go identifier
// (starts with a digit), which forces generateMainGo's go/format.Source call to
// fail — covering the "generate main.go" error-wrapping branch in runGenerate.
func invalidIdentifierSkill() map[string]string {
	return map[string]string{
		"SKILL.md": `---
name: invalid-identifier
description: Arg name is not a valid Go identifier.
metadata:
  aisotropy:
    required-scope: exec:bad
    scripts:
      - path: scripts/bad.py
        map-to: typed-tool
        tool-name: bad_tool
        args:
          - name: "1"
            type: string
            required: true
            description: Starts with a digit, invalid as a Go field name.
---
Bad tool.
`,
		"scripts/bad.py": "print(\"bad\")\n",
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newSkillDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

// ---------------------------------------------------------------------------
// run() argument parsing / usage
// ---------------------------------------------------------------------------

func TestRun_UsageErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no args", nil, "usage: skill-import"},
		{"too many positional", []string{"a", "b"}, "usage: skill-import"},
		{"-out missing value", []string{"-out"}, "-out requires a directory argument"},
		{"--out missing value", []string{"--out"}, "-out requires a directory argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tc.args, &out, &errb)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr=%q)", code, errb.String())
			}
			if !strings.Contains(errb.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", errb.String(), tc.wantErr)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout should be empty on a usage error, got %q", out.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Preview mode
// ---------------------------------------------------------------------------

func TestRun_PreviewMode_Success(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantAll []string
	}{
		{
			name:  "full typed skill",
			files: fullTypedSkill(),
			wantAll: []string{
				"Agent Skill -> aisotropy mapping",
				"skill:        full-typed",
				"license:      MIT",
				"WithTypedTool[DoThingArgs]",
				"references/notes.md",
				"assets/extra.txt",
				"references/undeclared.md",
				"(content) ingest as KindDocument [not in manifest]",
				"Run with -out <dir> to WRITE a compilable Go module",
			},
		},
		{
			name:  "mcp default mapping",
			files: mcpDefaultSkill(),
			wantAll: []string{
				"skill:        mcp-default",
				"license:      -",
				"WithMCPServer(\"exec:mcp\", \"bash\"",
				"WithMCPServer(\"exec:mcp\", \"<interpreter>\"",
			},
		},
		{
			name:  "gate1 refusal note (preview only warns)",
			files: gate1RefusalSkill(),
			wantAll: []string{
				"REFUSAL/HOLD: a script maps to a tool with an EMPTY required scope",
			},
		},
		{
			name:  "gate2 silent-injection note",
			files: gate2RefusalSkill(),
			wantAll: []string{
				"NOTE: instructions imply auto-load/always-on activation",
			},
		},
		{
			name:  "agpl license gate",
			files: agplLicenseSkill(),
			wantAll: []string{
				"LICENSE GATE (constitutional): AGPL-3.0 is on the block list",
			},
		},
		{
			name:  "sspl license gate",
			files: sspLicenseSkill(),
			wantAll: []string{
				"LICENSE GATE (constitutional): Server Side Public License (SSPL) is on the block list",
			},
		},
		{
			name:  "backtick body",
			files: backtickBodySkill(),
			wantAll: []string{
				"skill:        backtick-body",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newSkillDir(t, tc.files)
			var out, errb bytes.Buffer
			code := run([]string{dir}, &out, &errb)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errb.String())
			}
			if errb.Len() != 0 {
				t.Fatalf("stderr should be empty, got %q", errb.String())
			}
			for _, want := range tc.wantAll {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout missing %q\n--- full stdout ---\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRun_PreviewMode_ParseErrors(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name:    "missing SKILL.md",
			files:   map[string]string{},
			wantErr: "read ",
		},
		{
			name: "missing frontmatter fence",
			files: map[string]string{
				"SKILL.md": "no frontmatter here\n",
			},
			wantErr: "missing YAML frontmatter",
		},
		{
			name: "unterminated frontmatter",
			files: map[string]string{
				"SKILL.md": "---\nname: x\nno closing fence\n",
			},
			wantErr: "unterminated YAML frontmatter",
		},
		{
			name: "invalid yaml",
			files: map[string]string{
				"SKILL.md": "---\nname: [unclosed\n---\nbody\n",
			},
			wantErr: "parse frontmatter",
		},
		{
			name: "missing name field",
			files: map[string]string{
				"SKILL.md": "---\ndescription: no name here\n---\nbody\n",
			},
			wantErr: "frontmatter 'name' is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newSkillDir(t, tc.files)
			var out, errb bytes.Buffer
			code := run([]string{dir}, &out, &errb)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
			}
			if !strings.Contains(errb.String(), "skill-import: ") || !strings.Contains(errb.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want to contain %q", errb.String(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Generate mode: success paths
// ---------------------------------------------------------------------------

func TestRun_GenerateMode_Success(t *testing.T) {
	cases := []struct {
		name          string
		files         map[string]string
		wantFiles     []string
		wantNoFiles   []string
		wantReportAll []string
	}{
		{
			name:      "full typed skill",
			files:     fullTypedSkill(),
			wantFiles: []string{"go.mod", "go.sum", "main.go", "knowledge.go", "scripts/do_thing.py", "knowledge/notes.md"},
			wantReportAll: []string{
				`generated a compilable consumer module for "full-typed"`,
				"tools:    1 typed",
				"refs:     1 ingested-knowledge doc(s)",
				"verify:  (cd ",
			},
		},
		{
			name:      "mcp default mapping",
			files:     mcpDefaultSkill(),
			wantFiles: []string{"go.mod", "go.sum", "main.go", "scripts/run-thing.sh", "scripts/no-interp.sh"},
			wantNoFiles: []string{
				"knowledge.go",
			},
			wantReportAll: []string{
				"tools:    0 typed",
				"refs:     0 ingested-knowledge doc(s)",
			},
		},
		{
			name:      "silent phrasing but has declared content (must not refuse)",
			files:     silentWithContentSkill(),
			wantFiles: []string{"go.mod", "go.sum", "main.go", "knowledge.go", "knowledge/info.md"},
		},
		{
			name:      "backtick body uses quoted string fallback",
			files:     backtickBodySkill(),
			wantFiles: []string{"go.mod", "go.sum", "main.go", "scripts/run.py"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skillDir := newSkillDir(t, tc.files)
			outDir := filepath.Join(t.TempDir(), "out")
			var out, errb bytes.Buffer
			code := run([]string{"-out", outDir, skillDir}, &out, &errb)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, out.String(), errb.String())
			}
			if errb.Len() != 0 {
				t.Fatalf("stderr should be empty, got %q", errb.String())
			}
			for _, rel := range tc.wantFiles {
				if _, err := os.Stat(filepath.Join(outDir, rel)); err != nil {
					t.Errorf("expected generated file %s: %v", rel, err)
				}
			}
			for _, rel := range tc.wantNoFiles {
				if _, err := os.Stat(filepath.Join(outDir, rel)); err == nil {
					t.Errorf("did not expect generated file %s", rel)
				}
			}
			for _, want := range tc.wantReportAll {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout missing %q\n--- full stdout ---\n%s", want, out.String())
				}
			}
		})
	}
}

// TestRun_GenerateMode_LongFlags covers the "--out" and "--out=" argument forms
// (the "-out" and "-out=" forms are exercised by the other generate tests).
func TestRun_GenerateMode_LongFlags(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())

	t.Run("--out dir", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "out")
		var out, errb bytes.Buffer
		code := run([]string{"--out", outDir, skillDir}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errb.String())
		}
	})

	t.Run("-out=dir", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "out")
		var out, errb bytes.Buffer
		code := run([]string{"-out=" + outDir, skillDir}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errb.String())
		}
	})

	t.Run("--out=dir", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "out")
		var out, errb bytes.Buffer
		code := run([]string{"--out=" + outDir, skillDir}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, errb.String())
		}
	})
}

// ---------------------------------------------------------------------------
// Generate mode: refusal gates (ADR-0022 anti-magic)
// ---------------------------------------------------------------------------

func TestRun_GenerateMode_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantErr []string
	}{
		{
			name:  "gate 1: ungated script tool",
			files: gate1RefusalSkill(),
			wantErr: []string{
				"REFUSED (ADR-0022)",
				"required-scope is empty",
			},
		},
		{
			name:  "gate 2: inject-only prose with no content",
			files: gate2RefusalSkill(),
			wantErr: []string{
				"REFUSED (ADR-0022)",
				"presume silent always-on injection",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skillDir := newSkillDir(t, tc.files)
			outDir := filepath.Join(t.TempDir(), "out")
			var out, errb bytes.Buffer
			code := run([]string{"-out", outDir, skillDir}, &out, &errb)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(errb.String(), want) {
					t.Errorf("stderr = %q, want substring %q", errb.String(), want)
				}
			}
			if _, err := os.Stat(outDir); err == nil {
				t.Errorf("refused generation must not create the out dir")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Generate mode: parse error surfaced through runGenerate's own return path
// (distinct statement coverage from the preview-mode parse-error tests above).
// ---------------------------------------------------------------------------

func TestRun_GenerateMode_ParseError(t *testing.T) {
	skillDir := newSkillDir(t, map[string]string{}) // no SKILL.md
	outDir := filepath.Join(t.TempDir(), "out")
	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr=%q)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "skill-import: read ") {
		t.Fatalf("stderr = %q, want a read error", errb.String())
	}
}

// ---------------------------------------------------------------------------
// Generate mode: filesystem/tooling failure branches
// ---------------------------------------------------------------------------

func TestRun_GenerateMode_MissingScriptFile(t *testing.T) {
	skillDir := newSkillDir(t, missingScriptFileSkill())
	outDir := filepath.Join(t.TempDir(), "out")
	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "copy script") {
		t.Fatalf("stderr = %q, want a copy-script error", errb.String())
	}
}

func TestRun_GenerateMode_MissingReferenceFile(t *testing.T) {
	skillDir := newSkillDir(t, missingReferenceFileSkill())
	outDir := filepath.Join(t.TempDir(), "out")
	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "copy reference") {
		t.Fatalf("stderr = %q, want a copy-reference error", errb.String())
	}
}

func TestRun_GenerateMode_MkdirAllFailure(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	// blocker is a regular file, so os.MkdirAll on a path below it must fail.
	outDir := filepath.Join(blocker, "child")

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "create out dir") {
		t.Fatalf("stderr = %q, want a create-out-dir error", errb.String())
	}
}

func TestRun_GenerateMode_WriteGoModFailure(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())
	outDir := filepath.Join(t.TempDir(), "readonly-out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out dir: %v", err)
	}
	if err := os.Chmod(outDir, 0o555); err != nil {
		t.Fatalf("chmod out dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(outDir, 0o755) }) // let t.TempDir() clean up

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "write go.mod") {
		t.Fatalf("stderr = %q, want a write-go.mod error", errb.String())
	}
}

func TestRun_GenerateMode_InvalidGeneratedIdentifier(t *testing.T) {
	skillDir := newSkillDir(t, invalidIdentifierSkill())
	outDir := filepath.Join(t.TempDir(), "out")
	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "generate main.go") {
		t.Fatalf("stderr = %q, want a generate-main.go error", errb.String())
	}
}

// TestRun_GenerateMode_GoToolUnavailable forces `go mod tidy` to fail by hiding
// the go binary from PATH, covering the "go mod tidy ... failed" wrap branch.
func TestRun_GenerateMode_GoToolUnavailable(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())
	outDir := filepath.Join(t.TempDir(), "out")

	emptyPath := t.TempDir() // a directory guaranteed to contain no "go" binary
	t.Setenv("PATH", emptyPath)

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "go mod tidy") {
		t.Fatalf("stderr = %q, want a go-mod-tidy error", errb.String())
	}
}

// TestRun_GenerateMode_RepoRootNotFound covers findRepoRoot's failure branch by
// running from a directory with no aisotropy go.mod anywhere in its ancestry.
func TestRun_GenerateMode_RepoRootNotFound(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())
	outDir := filepath.Join(t.TempDir(), "out")

	outsideRepo := t.TempDir()
	t.Chdir(outsideRepo)

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "could not locate the aisotropy repo root") {
		t.Fatalf("stderr = %q, want a repo-root-not-found error", errb.String())
	}
}

// TestRun_GenerateMode_EmptyPathEntriesSkipped covers the "skip this entry, it
// declares no path" continue branches in both the script-copy and the
// reference-copy loops.
func TestRun_GenerateMode_EmptyPathEntriesSkipped(t *testing.T) {
	skillDir := newSkillDir(t, emptyPathEntriesSkill())
	outDir := filepath.Join(t.TempDir(), "out")
	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	// The real script/reference still land; the empty-path entries produce
	// nothing to copy (no crash, no phantom file).
	if _, err := os.Stat(filepath.Join(outDir, "scripts", "real.sh")); err != nil {
		t.Errorf("expected scripts/real.sh to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "knowledge", "real.md")); err != nil {
		t.Errorf("expected knowledge/real.md to be copied: %v", err)
	}
}

// TestRun_GenerateMode_WriteMainGoFailure forces the os.WriteFile(main.go) call
// to fail by pre-creating "main.go" as a directory inside outDir, so the write
// collides with an existing directory of the same name.
func TestRun_GenerateMode_WriteMainGoFailure(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill()) // no references, keeps this isolated to the main.go write
	outDir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(outDir, "main.go"), 0o755); err != nil {
		t.Fatalf("pre-create main.go directory: %v", err)
	}

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "write main.go") {
		t.Fatalf("stderr = %q, want a write-main.go error", errb.String())
	}
}

// TestRun_GenerateMode_WriteKnowledgeGoFailure forces the os.WriteFile
// (knowledge.go) call to fail the same way, isolated from the main.go write by
// using a skill dir that leaves "main.go" free.
func TestRun_GenerateMode_WriteKnowledgeGoFailure(t *testing.T) {
	skillDir := newSkillDir(t, fullTypedSkill()) // has a declared reference -> knowledge.go is generated
	outDir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(outDir, "knowledge.go"), 0o755); err != nil {
		t.Fatalf("pre-create knowledge.go directory: %v", err)
	}

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "write knowledge.go") {
		t.Fatalf("stderr = %q, want a write-knowledge.go error", errb.String())
	}
	// main.go must have written fine before the knowledge.go write collided.
	if _, err := os.Stat(filepath.Join(outDir, "main.go")); err != nil {
		t.Errorf("expected main.go to have been written before the failure: %v", err)
	}
}

// TestRun_GenerateMode_CopyFileMkdirAllFailure forces copyFile's own
// os.MkdirAll(filepath.Dir(dst), ...) call to fail by pre-creating the
// destination's parent directory name ("scripts") as a regular file.
func TestRun_GenerateMode_CopyFileMkdirAllFailure(t *testing.T) {
	skillDir := newSkillDir(t, mcpDefaultSkill())
	outDir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out dir: %v", err)
	}
	// "scripts" exists as a regular file, blocking copyFile's MkdirAll for
	// outDir/scripts/<script-name>.
	if err := os.WriteFile(filepath.Join(outDir, "scripts"), []byte("blocker"), 0o644); err != nil {
		t.Fatalf("write scripts blocker file: %v", err)
	}

	var out, errb bytes.Buffer
	code := run([]string{"-out", outDir, skillDir}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "copy script") {
		t.Fatalf("stderr = %q, want a copy-script error", errb.String())
	}
}
