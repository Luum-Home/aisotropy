package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// A golden dataset is a small, curated, checked-in set of labeled cases — the
// reference the harness grades against and the regression baseline a change is
// measured on. Two shapes ship as JSON (internal/eval/testdata): a generation
// set (EvalCase, for the judges) and a retrieval set (RetrievalCase, for
// EvalRetrieval). The loaders below are the wire contract: the JSON field
// names are the exported struct's json tags, so the on-disk gold set and the
// in-code types can never silently drift.

// generationDataset is the on-disk shape of a generation golden file: a named
// set of EvalCase records. Kept as its own DTO (not EvalCase reused directly)
// so the file can carry a Name/Description header alongside the cases.
type generationDataset struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Cases       []jsonCase `json:"cases"`
}

// jsonCase is the JSON projection of one EvalCase. Sample fields are inlined so
// the file stays flat and readable.
type jsonCase struct {
	Name     string             `json:"name"`
	Query    string             `json:"query"`
	Context  string             `json:"context"`
	Response string             `json:"response"`
	Expected map[string]float64 `json:"expected,omitempty"`
}

// retrievalDataset is the on-disk shape of a retrieval golden file.
type retrievalDataset struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Cases       []jsonRetrieval `json:"cases"`
}

// jsonRetrieval is the JSON projection of one RetrievalCase.
type jsonRetrieval struct {
	Name        string   `json:"name"`
	Query       string   `json:"query"`
	RelevantIDs []string `json:"relevant_ids"`
}

// LoadEvalCases decodes a generation golden dataset (as written in
// testdata/*.json) into EvalCase records. It fails closed on malformed JSON or
// an empty case set — an empty gold set graded to a green report would be a
// silent lie.
func LoadEvalCases(r io.Reader) ([]EvalCase, error) {
	var ds generationDataset
	if err := json.NewDecoder(r).Decode(&ds); err != nil {
		return nil, fmt.Errorf("eval: decode generation dataset: %w", err)
	}
	if len(ds.Cases) == 0 {
		return nil, fmt.Errorf("eval: generation dataset %q has no cases", ds.Name)
	}
	cases := make([]EvalCase, len(ds.Cases))
	for i, c := range ds.Cases {
		cases[i] = EvalCase{
			Name:     c.Name,
			Sample:   EvalSample{Query: c.Query, Context: c.Context, Response: c.Response},
			Expected: c.Expected,
		}
	}
	return cases, nil
}

// LoadRetrievalCases decodes a retrieval golden dataset into RetrievalCase
// records, fail-closed on malformed JSON or an empty case set.
func LoadRetrievalCases(r io.Reader) ([]RetrievalCase, error) {
	var ds retrievalDataset
	if err := json.NewDecoder(r).Decode(&ds); err != nil {
		return nil, fmt.Errorf("eval: decode retrieval dataset: %w", err)
	}
	if len(ds.Cases) == 0 {
		return nil, fmt.Errorf("eval: retrieval dataset %q has no cases", ds.Name)
	}
	cases := make([]RetrievalCase, len(ds.Cases))
	for i, c := range ds.Cases {
		cases[i] = RetrievalCase{Name: c.Name, Query: c.Query, RelevantIDs: c.RelevantIDs}
	}
	return cases, nil
}

// LoadEvalCasesFile is the path-based convenience over LoadEvalCases.
func LoadEvalCasesFile(path string) ([]EvalCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: open generation dataset: %w", err)
	}
	defer func() { _ = f.Close() }()
	return LoadEvalCases(f)
}

// LoadRetrievalCasesFile is the path-based convenience over LoadRetrievalCases.
func LoadRetrievalCasesFile(path string) ([]RetrievalCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: open retrieval dataset: %w", err)
	}
	defer func() { _ = f.Close() }()
	return LoadRetrievalCases(f)
}
