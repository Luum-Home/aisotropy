package main

import (
	"path/filepath"
	"testing"
)

// TestDeclaredInfeasible_ReadsTheRealManifests is deliberately wired to the
// SHIPPED matrix.json files rather than a fixture.
//
// The bug this pins was not a wrong declaration — the manifests were right all
// along. eino, genkit-go, ai-sdk and pydantic-ai each declared
// isolation_forgetful `infeasible:<reason>` because none of them has a
// framework-owned scope primitive to omit; the scope in those adapters is
// hand-rolled glue, so a blank user id would exercise OUR code and get reported
// as the framework's behaviour. The runner simply never read the declarations:
// it built the work anyway, the adapter exited with `unknown mode`, and the cell
// landed in the report as an ERROR. Four such cells on the 2026-07-27 board,
// inflating the error rate with something that was never a failure.
//
// A declaration nobody reads is not a declaration. Reading the real files is the
// point of this test: a fixture would keep passing if a manifest changed.
func TestDeclaredInfeasible_ReadsTheRealManifests(t *testing.T) {
	docs, err := loadMatrixDocs(filepath.Join("..", ""))
	if err != nil {
		t.Fatalf("loadMatrixDocs: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no matrix.json manifests found — this test must run against the shipped ones")
	}
	got := declaredInfeasible(docs)

	for _, fw := range []string{"eino", "genkit-go", "ai-sdk", "pydantic-ai"} {
		reason, ok := got[fw]["isolation_forgetful"]
		if !ok {
			t.Errorf("%s: isolation_forgetful not recognised as declared-infeasible — "+
				"the runner would build the work and record `unknown mode` as an error", fw)
			continue
		}
		if reason == "" {
			t.Errorf("%s: declared infeasible with an EMPTY reason; a gap without a reason is a blank cell", fw)
		}
	}

	// The counterweight: a framework that genuinely implements the dimension must
	// NOT be skipped. Over-skipping would silently drop real coverage — the same
	// damage as the bug, in the other direction.
	for _, fw := range []string{"aisotropy", "mastra"} {
		if _, skipped := got[fw]["isolation_forgetful"]; skipped {
			t.Errorf("%s implements isolation_forgetful but was marked infeasible — coverage would be dropped", fw)
		}
	}
}

// TestDeclaredInfeasible_RequiresEveryCell — one non-infeasible cell anywhere in
// a dimension means the work still gets built. Being conservative here matters:
// wrongly skipping is invisible in the report, whereas wrongly running produces a
// loud error someone will investigate.
func TestDeclaredInfeasible_RequiresEveryCell(t *testing.T) {
	mixed := matrixDoc{
		Framework: "mixed",
		Dimensions: map[string]matrixLanes{
			"d": {Stub: matrixForms{Native: "infeasible:no scope", Docker: "pass"}},
		},
	}
	if _, ok := declaredInfeasible([]matrixDoc{mixed})["mixed"]["d"]; ok {
		t.Error("a dimension with one passing cell must NOT count as declared-infeasible")
	}

	allInf := matrixDoc{
		Framework: "allinf",
		Dimensions: map[string]matrixLanes{
			"d": {Stub: matrixForms{Native: "infeasible:no scope", Docker: "infeasible:no scope"}},
		},
	}
	if r, ok := declaredInfeasible([]matrixDoc{allInf})["allinf"]["d"]; !ok || r != "no scope" {
		t.Errorf("all-infeasible dimension not recognised (ok=%v reason=%q)", ok, r)
	}
}
