package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// A question set is a small, curated, checked-in (or operator-supplied) list of
// questions to run END-TO-END through the full aisotropy turn pipeline and then
// grade with an LLM-as-a-judge. Two shapes ship:
//
//   - testdata/generic.json — a bundled GENERIC smoke set (general-knowledge
//     Q&A, some carrying context_docs). It proves the pipeline + judge wire up
//     end-to-end. It is NOT a measure of an adopter's real answer quality.
//   - testdata/domain.example.json — a TEMPLATE the operator copies and fills
//     with THEIR real domain questions (and, where they have them, ideal answers
//     and the knowledge documents the answer should be grounded in). The domain
//     set is the ONLY measure that reflects production quality; the generic set
//     is a wiring smoke, nothing more.
//
// The on-disk JSON tags are the wire contract; the exported/embedded field tags
// below are the single source of truth so the file and the code cannot drift.

// questionSet is the on-disk shape of a question file.
type questionSet struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Questions   []jsonQuestion `json:"questions"`
}

// jsonQuestion is one question. Only `question` is required. `ideal_answer`
// enables the correctness judge to compare against a gold target; `context_docs`
// are the knowledge documents seeded into the memory tier before the run, so the
// turn's REAL RAG hydration can retrieve them (and the faithfulness judge can
// check the answer against them).
type jsonQuestion struct {
	ID          string        `json:"id"`
	Question    string        `json:"question"`
	IdealAnswer string        `json:"ideal_answer,omitempty"`
	ContextDocs []jsonContext `json:"context_docs,omitempty"`
}

// jsonContext is one knowledge document attached to a question.
type jsonContext struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
}

// question is the in-memory projection the pipeline runs: the prompt, the
// optional ideal answer, the concatenated context (grounding shown to the
// faithfulness judge), and the raw docs (seeded into knowledge).
type question struct {
	ID      string
	Prompt  string
	Ideal   string
	Context string
	Docs    []contextDoc
}

// contextDoc is one knowledge document to seed.
type contextDoc struct {
	ID     string
	Text   string
	Source string
}

// loadQuestions decodes a question set and projects it into question records.
// It fails closed on malformed JSON, an empty set, or a question with an empty
// prompt — an empty/blank set graded to a green report would be a silent lie.
func loadQuestions(r io.Reader) (string, []question, error) {
	var qs questionSet
	if err := json.NewDecoder(r).Decode(&qs); err != nil {
		return "", nil, fmt.Errorf("answer-quality: decode question set: %w", err)
	}
	if len(qs.Questions) == 0 {
		return "", nil, fmt.Errorf("answer-quality: question set %q has no questions", qs.Name)
	}
	out := make([]question, 0, len(qs.Questions))
	for i, jq := range qs.Questions {
		if strings.TrimSpace(jq.Question) == "" {
			return "", nil, fmt.Errorf("answer-quality: question set %q: question #%d has an empty prompt", qs.Name, i+1)
		}
		id := jq.ID
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		docs := make([]contextDoc, 0, len(jq.ContextDocs))
		var ctx strings.Builder
		for j, jc := range jq.ContextDocs {
			did := jc.ID
			if did == "" {
				did = fmt.Sprintf("%s-doc%d", id, j+1)
			}
			docs = append(docs, contextDoc{ID: did, Text: jc.Text, Source: jc.Source})
			if strings.TrimSpace(jc.Text) != "" {
				if ctx.Len() > 0 {
					ctx.WriteString("\n\n")
				}
				ctx.WriteString(jc.Text)
			}
		}
		out = append(out, question{
			ID:      id,
			Prompt:  jq.Question,
			Ideal:   jq.IdealAnswer,
			Context: ctx.String(),
			Docs:    docs,
		})
	}
	return qs.Name, out, nil
}

// loadQuestionsFile is the path-based convenience over loadQuestions.
func loadQuestionsFile(path string) (string, []question, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("answer-quality: open question set: %w", err)
	}
	defer func() { _ = f.Close() }()
	return loadQuestions(f)
}
