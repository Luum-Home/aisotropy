package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- validMatrixStatus -------------------------------------------------------

func TestMatrixStatus_ValidVocabulary(t *testing.T) {
	valid := []string{"pass", "fail", "pending", "infeasible:WASI has no sockets"}
	for _, s := range valid {
		if !validMatrixStatus(s) {
			t.Errorf("validMatrixStatus(%q) = false, want true", s)
		}
	}
}

func TestMatrixStatus_InvalidVocabulary(t *testing.T) {
	invalid := []string{"", "n/a", "skip", "todo", "unknown", "PASS", "infeasible", "infeasible:"}
	for _, s := range invalid {
		if validMatrixStatus(s) {
			t.Errorf("validMatrixStatus(%q) = true, want false", s)
		}
	}
}

// ---- gateMatrices ------------------------------------------------------------

func fullCoverageDoc(fw string) matrixDoc {
	pass := matrixForms{Native: statusPass, Docker: statusPass, Wasm: "infeasible:WASI has no sockets — the framework's HTTP model client cannot run in-sandbox"}
	return matrixDoc{
		Framework: fw,
		Dimensions: map[string]matrixLanes{
			"qa": {Stub: pass, Real: pass},
			"rag": {
				Stub: matrixForms{Native: statusPass, Docker: statusFail, Wasm: "infeasible:WASI has no sockets"},
				Real: matrixForms{Native: statusPass, Docker: statusPass, Wasm: "infeasible:WASI has no sockets"},
			},
		},
	}
}

func TestMatrixGate_PendingCellFails(t *testing.T) {
	doc := fullCoverageDoc("acme")
	// Poke one cell back to pending — this is the exact defect class the gate
	// exists to catch: a silent blank hiding behind an otherwise-complete file.
	lanes := doc.Dimensions["qa"]
	lanes.Real.Wasm = statusPending
	doc.Dimensions["qa"] = lanes

	err := gateMatrices([]matrixDoc{doc})
	if err == nil {
		t.Fatal("gateMatrices() = nil, want error for a pending cell")
	}
	if !strings.Contains(err.Error(), "acme / qa / real / wasm") {
		t.Errorf("gateMatrices() error = %q, want it to name the exact gap (acme / qa / real / wasm)", err.Error())
	}
	if !strings.Contains(err.Error(), "1 matrix cell") {
		t.Errorf("gateMatrices() error = %q, want a count of 1", err.Error())
	}
}

func TestMatrixGate_PassFailInfeasibleOnlyPasses(t *testing.T) {
	doc := fullCoverageDoc("acme")
	if err := gateMatrices([]matrixDoc{doc}); err != nil {
		t.Fatalf("gateMatrices() = %v, want nil for a matrix with only pass/fail/infeasible cells", err)
	}
}

func TestMatrixGate_EmptyDocsPassesTrivially(t *testing.T) {
	if err := gateMatrices(nil); err != nil {
		t.Fatalf("gateMatrices(nil) = %v, want nil — matrix.json is opt-in until an adapter's fill pass lands", err)
	}
}

func TestMatrixGate_InvalidStatusFails(t *testing.T) {
	doc := fullCoverageDoc("acme")
	lanes := doc.Dimensions["rag"]
	lanes.Stub.Native = "maybe" // not in the closed vocabulary
	doc.Dimensions["rag"] = lanes

	err := gateMatrices([]matrixDoc{doc})
	if err == nil {
		t.Fatal("gateMatrices() = nil, want error for an invalid status string")
	}
	if !strings.Contains(err.Error(), `invalid status "maybe"`) {
		t.Errorf("gateMatrices() error = %q, want it to quote the invalid status", err.Error())
	}
}

func TestMatrixGate_MultipleFrameworksAggregateGaps(t *testing.T) {
	good := fullCoverageDoc("acme")
	bad := fullCoverageDoc("beta")
	lanes := bad.Dimensions["rag"]
	lanes.Stub.Docker = statusPending
	bad.Dimensions["rag"] = lanes

	err := gateMatrices([]matrixDoc{good, bad})
	if err == nil {
		t.Fatal("gateMatrices() = nil, want error — beta has a pending cell")
	}
	if strings.Contains(err.Error(), "acme /") {
		t.Errorf("gateMatrices() error = %q, want no gaps reported for acme (fully covered)", err.Error())
	}
	if !strings.Contains(err.Error(), "beta / rag / stub / docker") {
		t.Errorf("gateMatrices() error = %q, want the beta gap named", err.Error())
	}
}

// ---- loadMatrixDocs -----------------------------------------------------------

func TestMatrixLoad_DiscoversAndParses(t *testing.T) {
	dir := t.TempDir()
	fwDir := filepath.Join(dir, "acme")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := fullCoverageDoc("acme")
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fwDir, "matrix.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	docs, err := loadMatrixDocs(dir)
	if err != nil {
		t.Fatalf("loadMatrixDocs() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("loadMatrixDocs() returned %d doc(s), want 1", len(docs))
	}
	if docs[0].Framework != "acme" {
		t.Errorf("docs[0].Framework = %q, want %q", docs[0].Framework, "acme")
	}
	if len(docs[0].Dimensions) != 2 {
		t.Errorf("docs[0].Dimensions has %d entries, want 2", len(docs[0].Dimensions))
	}
}

func TestMatrixLoad_NoManifestsIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	docs, err := loadMatrixDocs(dir)
	if err != nil {
		t.Fatalf("loadMatrixDocs() error = %v, want nil for an empty live dir", err)
	}
	if len(docs) != 0 {
		t.Errorf("loadMatrixDocs() returned %d doc(s), want 0", len(docs))
	}
}

func TestMatrixLoad_FrameworkNameMismatchErrors(t *testing.T) {
	dir := t.TempDir()
	fwDir := filepath.Join(dir, "acme")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "framework" says "wrong-name" but lives under the "acme" directory.
	doc := fullCoverageDoc("wrong-name")
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(fwDir, "matrix.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadMatrixDocs(dir); err == nil {
		t.Fatal("loadMatrixDocs() = nil error, want error for a framework/directory name mismatch")
	}
}

// ---- rendering -----------------------------------------------------------------

func TestMatrixRender_InfeasibleReasonsRenderInline(t *testing.T) {
	doc := fullCoverageDoc("acme")
	rep := reportData{Frameworks: []string{"acme"}, MatrixDocs: []matrixDoc{doc}}

	var b strings.Builder
	writeMatrixSection(&b, rep)
	out := b.String()

	if !strings.Contains(out, "WASI has no sockets") {
		t.Errorf("writeMatrixSection() output missing the infeasible reason text; got:\n%s", out)
	}
	if !strings.Contains(out, "infeasible:") {
		t.Errorf("writeMatrixSection() output missing the \"infeasible:\" prefix on the rendered cell; got:\n%s", out)
	}
	if !strings.Contains(out, "`qa`") || !strings.Contains(out, "`rag`") {
		t.Errorf("writeMatrixSection() output missing dimension rows; got:\n%s", out)
	}
}

func TestMatrixRender_MissingManifestIsNamedNotSkipped(t *testing.T) {
	rep := reportData{Frameworks: []string{"acme", "no-manifest-fw"}, MatrixDocs: []matrixDoc{fullCoverageDoc("acme")}}

	var b strings.Builder
	writeMatrixSection(&b, rep)
	out := b.String()

	if !strings.Contains(out, "no-manifest-fw") {
		t.Errorf("writeMatrixSection() output does not mention the manifest-less framework; got:\n%s", out)
	}
	if !strings.Contains(out, "no `matrix.json` yet") {
		t.Errorf("writeMatrixSection() output does not flag the missing manifest explicitly; got:\n%s", out)
	}
}

func TestMatrixRender_PendingCellRendersLoudlyWhenGateSkipped(t *testing.T) {
	doc := fullCoverageDoc("acme")
	lanes := doc.Dimensions["qa"]
	lanes.Real.Wasm = statusPending
	doc.Dimensions["qa"] = lanes
	rep := reportData{Frameworks: []string{"acme"}, MatrixDocs: []matrixDoc{doc}}

	var b strings.Builder
	writeMatrixSection(&b, rep)
	out := b.String()

	if !strings.Contains(out, "pending") {
		t.Errorf("writeMatrixSection() output does not surface the pending cell; got:\n%s", out)
	}
}
