package main

import (
	"strings"
	"testing"
)

func TestLoadQuestions_ProjectsAndDefaults(t *testing.T) {
	const blob = `{
      "name": "t",
      "questions": [
        {"id": "a", "question": "Q1?", "ideal_answer": "A1",
         "context_docs": [{"text": "doc one"}, {"id": "d2", "text": "doc two", "source": "s"}]},
        {"question": "Q2?"}
      ]
    }`
	name, qs, err := loadQuestions(strings.NewReader(blob))
	if err != nil {
		t.Fatalf("loadQuestions: %v", err)
	}
	if name != "t" {
		t.Errorf("name = %q; want t", name)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d questions; want 2", len(qs))
	}
	// Context is the concatenation of the doc texts.
	if qs[0].Context != "doc one\n\ndoc two" {
		t.Errorf("context = %q; want the two docs joined", qs[0].Context)
	}
	// A doc with no id gets a deterministic default; the explicit id is kept.
	if qs[0].Docs[0].ID != "a-doc1" || qs[0].Docs[1].ID != "d2" {
		t.Errorf("doc ids = %q,%q; want a-doc1,d2", qs[0].Docs[0].ID, qs[0].Docs[1].ID)
	}
	// A question with no id gets a positional default.
	if qs[1].ID != "q2" {
		t.Errorf("second question id = %q; want q2", qs[1].ID)
	}
	if qs[1].Context != "" {
		t.Errorf("second question should have no context; got %q", qs[1].Context)
	}
}

func TestLoadQuestions_FailsClosed(t *testing.T) {
	t.Run("empty-set", func(t *testing.T) {
		if _, _, err := loadQuestions(strings.NewReader(`{"name":"x","questions":[]}`)); err == nil {
			t.Fatal("want error for an empty question set")
		}
	})
	t.Run("blank-prompt", func(t *testing.T) {
		if _, _, err := loadQuestions(strings.NewReader(`{"questions":[{"question":"   "}]}`)); err == nil {
			t.Fatal("want error for a blank prompt")
		}
	})
	t.Run("malformed-json", func(t *testing.T) {
		if _, _, err := loadQuestions(strings.NewReader(`{not json`)); err == nil {
			t.Fatal("want error for malformed JSON")
		}
	})
}
