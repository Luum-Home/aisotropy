// Command scopecheck runs the scopecheck analyzer (ADR-0011 residual-gap
// enforcement: no bare facts.Scope{...}/semantic.Scope{...} literals outside
// their defining package or tests) as a standalone go/analysis driver:
//
//	go run ./tools/analyzers/scopecheck/cmd/scopecheck ./...
//
// It exits non-zero if any diagnostic is reported, matching go vet's
// contract, so it can be wired into CI or a pre-commit gate the same way.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/Luum-Home/aisotropy/tools/analyzers/scopecheck"
)

func main() {
	singlechecker.Main(scopecheck.Analyzer)
}
