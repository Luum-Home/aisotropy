package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Luum-Home/aisotropy/internal/eval"
)

// goldSet is the on-disk shape of the self-contained multilingual retrieval
// slice (testdata/xquad_slice.json, produced by scripts/fetch_xquad.py). It
// carries the corpora (one deduplicated paragraph set per language) AND the
// per-slice qrels inline, so the harness needs no network and the dataset's
// license/attribution travel with the numbers it produces.
type goldSet struct {
	Name         string           `json:"name"`
	Source       string           `json:"source"`
	SourceURL    string           `json:"source_url"`
	License      string           `json:"license"`
	LicenseURL   string           `json:"license_url"`
	Attribution  string           `json:"attribution"`
	GeneratedBy  string           `json:"generated_by"`
	AlignedPairs int              `json:"aligned_pairs"`
	CorpusSizes  map[string]int   `json:"corpus_sizes"`
	Corpora      map[string][]doc `json:"corpora"`
	Slices       []goldSlice      `json:"slices"`
}

// doc is one corpus passage: a stable id and its text. The id is what a
// slice's qrels reference and what Store.Search returns, so retrieved and
// relevant ids are directly comparable.
type doc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// goldSlice is one retrieval task over the shared corpora: which language the
// queries are in, which language's corpus they search, and the labeled cases.
// The four slices (en/es monolingual + both cross-lingual directions) share the
// same two corpora — corpus_lang selects which one this slice retrieves against.
type goldSlice struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	QueryLang   string     `json:"query_lang"`
	CorpusLang  string     `json:"corpus_lang"`
	Cases       []goldCase `json:"cases"`
}

// goldCase is one labeled retrieval case, matching eval.RetrievalCase's wire
// shape (name, query, relevant_ids).
type goldCase struct {
	Name        string   `json:"name"`
	Query       string   `json:"query"`
	RelevantIDs []string `json:"relevant_ids"`
}

// loadGoldSet decodes the retrieval slice, failing closed on malformed JSON, an
// empty corpus, or a slice that references a corpus language the file does not
// carry (a qrel pointing at nothing would silently deflate every metric).
func loadGoldSet(path string) (*goldSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gold set: %w", err)
	}
	defer func() { _ = f.Close() }()

	var gs goldSet
	if err := json.NewDecoder(f).Decode(&gs); err != nil {
		return nil, fmt.Errorf("decode gold set: %w", err)
	}
	if len(gs.Corpora) == 0 {
		return nil, fmt.Errorf("gold set %q has no corpora", gs.Name)
	}
	if len(gs.Slices) == 0 {
		return nil, fmt.Errorf("gold set %q has no slices", gs.Name)
	}
	for _, s := range gs.Slices {
		if len(gs.Corpora[s.CorpusLang]) == 0 {
			return nil, fmt.Errorf("slice %q references empty/absent corpus %q", s.Name, s.CorpusLang)
		}
		if len(s.Cases) == 0 {
			return nil, fmt.Errorf("slice %q has no cases", s.Name)
		}
	}
	return &gs, nil
}

// evalCases converts a slice's goldCases into eval.RetrievalCase records for the
// shared internal/eval harness.
func (s goldSlice) evalCases() []eval.RetrievalCase {
	out := make([]eval.RetrievalCase, len(s.Cases))
	for i, c := range s.Cases {
		out[i] = eval.RetrievalCase{Name: c.Name, Query: c.Query, RelevantIDs: c.RelevantIDs}
	}
	return out
}
