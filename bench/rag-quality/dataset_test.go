package main

import (
	"os"
	"path/filepath"
	"testing"
)

const miniGoldPath = "testdata/mini_gold.json"

func TestLoadGoldSet_Success(t *testing.T) {
	gs, err := loadGoldSet(miniGoldPath)
	if err != nil {
		t.Fatalf("loadGoldSet: %v", err)
	}
	if gs.Name != "mini-test-gold" {
		t.Errorf("Name = %q, want mini-test-gold", gs.Name)
	}
	if len(gs.Corpora) != 2 {
		t.Fatalf("got %d corpora, want 2", len(gs.Corpora))
	}
	if len(gs.Corpora["en"]) != 2 || len(gs.Corpora["es"]) != 2 {
		t.Errorf("unexpected corpus sizes: en=%d es=%d", len(gs.Corpora["en"]), len(gs.Corpora["es"]))
	}
	if len(gs.Slices) != 2 {
		t.Fatalf("got %d slices, want 2", len(gs.Slices))
	}
}

func TestLoadGoldSet_FileNotFound(t *testing.T) {
	if _, err := loadGoldSet("testdata/does-not-exist.json"); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// writeTempJSON writes body to a fresh file in t.TempDir() and returns its
// path — the shared helper every malformed-input test below uses so each
// case is a self-contained, disposable fixture (no shared mutable file).
func writeTempJSON(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gold.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp fixture: %v", err)
	}
	return path
}

func TestLoadGoldSet_MalformedJSON(t *testing.T) {
	path := writeTempJSON(t, `{not valid json`)
	if _, err := loadGoldSet(path); err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
}

func TestLoadGoldSet_EmptyCorpora(t *testing.T) {
	path := writeTempJSON(t, `{"name":"x","corpora":{},"slices":[]}`)
	_, err := loadGoldSet(path)
	if err == nil {
		t.Fatal("expected an error for a gold set with no corpora")
	}
}

func TestLoadGoldSet_EmptySlices(t *testing.T) {
	body := `{
		"name": "x",
		"corpora": {"en": [{"id": "en-0", "text": "hello"}]},
		"slices": []
	}`
	path := writeTempJSON(t, body)
	_, err := loadGoldSet(path)
	if err == nil {
		t.Fatal("expected an error for a gold set with no slices")
	}
}

func TestLoadGoldSet_SliceReferencesEmptyCorpus(t *testing.T) {
	body := `{
		"name": "x",
		"corpora": {"en": [{"id": "en-0", "text": "hello"}]},
		"slices": [{"name": "s", "query_lang": "es", "corpus_lang": "es",
			"cases": [{"name": "c", "query": "q", "relevant_ids": ["en-0"]}]}]
	}`
	path := writeTempJSON(t, body)
	_, err := loadGoldSet(path)
	if err == nil {
		t.Fatal("expected an error for a slice referencing an absent corpus")
	}
}

func TestLoadGoldSet_SliceNoCases(t *testing.T) {
	body := `{
		"name": "x",
		"corpora": {"en": [{"id": "en-0", "text": "hello"}]},
		"slices": [{"name": "s", "query_lang": "en", "corpus_lang": "en", "cases": []}]
	}`
	path := writeTempJSON(t, body)
	_, err := loadGoldSet(path)
	if err == nil {
		t.Fatal("expected an error for a slice with no cases")
	}
}

func TestGoldSlice_EvalCases(t *testing.T) {
	s := goldSlice{
		Name: "s",
		Cases: []goldCase{
			{Name: "a", Query: "qa", RelevantIDs: []string{"id-1", "id-2"}},
			{Name: "b", Query: "qb", RelevantIDs: nil},
		},
	}
	cases := s.evalCases()
	if len(cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(cases))
	}
	if cases[0].Name != "a" || cases[0].Query != "qa" || len(cases[0].RelevantIDs) != 2 {
		t.Errorf("case 0 = %+v, unexpected projection", cases[0])
	}
	if cases[1].Name != "b" || cases[1].RelevantIDs != nil {
		t.Errorf("case 1 = %+v, unexpected projection", cases[1])
	}
}

func TestGoldSlice_EvalCases_Empty(t *testing.T) {
	s := goldSlice{}
	cases := s.evalCases()
	if len(cases) != 0 {
		t.Fatalf("got %d cases, want 0", len(cases))
	}
}
