// Package scopecheck implements a go/analysis Analyzer that closes the
// residual gap called out in ADR-0011 ("Reconciliation with the facts
// tier"): facts.Scope and semantic.Scope are still exported structs, so a
// bare composite literal such as facts.Scope{TenantID: t, UserID: u} still
// compiles even though the mandated construction path is facts.NewScope /
// facts.ScopeFromContext (which validate identity through internal/tenancy).
// The blessed-constructor rule was, until this analyzer, a convention
// enforced only by code review — this package turns it into a go-vet-style
// diagnostic.
//
// scopecheck flags a composite literal of a configured "pkgpath.TypeName"
// (by default github.com/Luum-Home/aisotropy/internal/facts.Scope and
// github.com/Luum-Home/aisotropy/internal/semantic.Scope) whenever it
// appears:
//
//   - outside the package that defines the type (that package's own files
//     legitimately construct the type it owns, including inside the
//     constructors themselves), and
//   - outside a _test.go file (tests are allowed to build scopes directly;
//     ADR-0011 explicitly carves this out).
//
// The type match is resolved through the type checker (pass.TypesInfo), not
// by comparing syntax/identifier names, so a local, unrelated type also
// named Scope never false-positives, and the check is robust to import
// aliases and elided composite-literal types (e.g. inside a slice literal).
package scopecheck

import (
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const doc = `report bare facts.Scope{...}/semantic.Scope{...} composite literals

scopecheck flags a composite literal constructing one of the configured
"pkgpath.TypeName" targets (default: the facts and semantic tiers' Scope
types) anywhere outside the type's own defining package or a _test.go file.
Per ADR-0011, tenant/user identity for the facts and semantic tiers must be
obtained through the type's blessed constructor (e.g. facts.NewScope /
facts.ScopeFromContext), which routes through internal/tenancy validation —
a bare literal bypasses that validation while still compiling.

Configure the target type list with -types, a comma-separated list of
"import/path.TypeName" pairs. Leaving -types unset uses the built-in default
(facts.Scope, semantic.Scope from this module).`

// defaultTargets is the built-in list of "<import path>.<TypeName>" pairs
// scopecheck flags when -types is left unset. It mirrors ADR-0011's residual
// gap for both tiers that share the exported-Scope shape.
var defaultTargets = []string{
	"github.com/Luum-Home/aisotropy/internal/facts.Scope",
	"github.com/Luum-Home/aisotropy/internal/semantic.Scope",
}

// typesFlagValue backs the -types flag. It is package-level (rather than
// analysis.Pass-scoped) because analysis.Analyzer.Flags is a single
// flag.FlagSet shared across passes/runs of the same Analyzer instance,
// exactly like other go/analysis analyzers (e.g. printf's -funcs).
var typesFlagValue string

// Analyzer is the scopecheck go/analysis.Analyzer. Run it standalone via
// tools/analyzers/scopecheck/cmd/scopecheck, or wire it into any
// multichecker/singlechecker-based driver.
var Analyzer = &analysis.Analyzer{
	Name:     "scopecheck",
	Doc:      doc,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func init() {
	Analyzer.Flags.Init("scopecheck", flag.ExitOnError)
	Analyzer.Flags.StringVar(&typesFlagValue, "types", "",
		"comma-separated list of pkgpath.TypeName targets to flag as bare composite literals "+
			"(default: facts.Scope and semantic.Scope from this module)")
}

// targetSet parses the -types flag (or falls back to defaultTargets) into a
// set keyed by "pkgpath.TypeName" for O(1) membership checks.
func targetSet() map[string]bool {
	list := defaultTargets
	if typesFlagValue != "" {
		list = strings.Split(typesFlagValue, ",")
	}
	set := make(map[string]bool, len(list))
	for _, t := range list {
		t = strings.TrimSpace(t)
		if t != "" {
			set[t] = true
		}
	}
	return set
}

func run(pass *analysis.Pass) (interface{}, error) {
	want := targetSet()
	if len(want) == 0 {
		return nil, nil //nolint:nilnil // analyzer contract: no Result, no error when there is nothing to check
	}

	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, fmt.Errorf("scopecheck: missing inspect.Analyzer result")
	}
	nodeFilter := []ast.Node{(*ast.CompositeLit)(nil)}

	insp.Preorder(nodeFilter, func(n ast.Node) {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return
		}

		named, qualified, ok := namedTargetType(pass, lit, want)
		if !ok {
			return
		}

		// Allow the type's own defining package to construct it directly —
		// that includes the blessed constructors themselves.
		if named.Obj().Pkg().Path() == pass.Pkg.Path() {
			return
		}

		// Allow test files anywhere: tests legitimately build scopes
		// directly instead of threading a validated context through.
		filename := pass.Fset.Position(lit.Pos()).Filename
		if strings.HasSuffix(filename, "_test.go") {
			return
		}

		pass.Reportf(lit.Pos(),
			"bare %s{...} composite literal is prohibited outside its defining package and tests; "+
				"use %s.NewScope(...) or %s.ScopeFromContext(ctx) instead (ADR-0011)",
			qualified, named.Obj().Pkg().Name(), named.Obj().Pkg().Name())
	})

	return nil, nil //nolint:nilnil // analyzer contract: diagnostics are reported via pass.Reportf, not returned
}

// namedTargetType resolves lit's type through the type checker and reports
// whether it matches one of the configured targets. Resolving via
// pass.TypesInfo (rather than inspecting lit.Type's AST syntax) means
// qualified references (facts.Scope{}), unqualified references from inside
// the defining package (Scope{}), and elided types in nested composite
// literals ([]facts.Scope{{...}}) are all handled uniformly, and a
// same-named type in an unrelated package never matches.
func namedTargetType(pass *analysis.Pass, lit *ast.CompositeLit, want map[string]bool) (*types.Named, string, bool) {
	t := pass.TypesInfo.TypeOf(lit)
	if t == nil {
		return nil, "", false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil, "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return nil, "", false
	}
	qualified := fmt.Sprintf("%s.%s", obj.Pkg().Path(), obj.Name())
	if !want[qualified] {
		return nil, "", false
	}
	return named, qualified, true
}
