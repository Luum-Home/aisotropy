package scopecheck_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/Luum-Home/aisotropy/tools/analyzers/scopecheck"
)

// TestAnalyzer runs scopecheck against the testdata/src/a fixture package,
// which imports two hermetic stand-in packages (testdata/src/facts,
// testdata/src/semantic) shaped like the real facts.Scope/semantic.Scope.
//
// Since the fixtures live at import paths "facts"/"semantic" rather than the
// real module's github.com/Luum-Home/aisotropy/internal/{facts,semantic},
// the -types flag is set explicitly for this run instead of relying on the
// built-in default target list (which names the real module's types).
func TestAnalyzer(t *testing.T) {
	if err := scopecheck.Analyzer.Flags.Set("types", "facts.Scope,semantic.Scope"); err != nil {
		t.Fatalf("setting -types flag: %v", err)
	}

	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, scopecheck.Analyzer, "a")
}
