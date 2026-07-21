package main

import (
	"testing"

	"aisotropy-live-runner/scoring"
)

// ---- DEFECT 1 regression: multi-hop RAG dispatch + pass rule ---------------
//
// Calibration finding (see PR/task description): rag_questions h4
// ("Which route does the Blue Guarantee not cover during the winter closure,
// and on what dates does that closure start and end?", supporting_docs
// ["08-blue-guarantee.md","04-halcyon-pass.md"], answer key
// ["Halcyon 1 December 1 March"]) FAILED overall even though its answer-key
// containment scored 1.00, and the rendered mode signal read "hit@1 ✗".
//
// INVESTIGATION CONCLUSION (outcome c — working as designed, not a bug):
//
//  1. Dispatch is correct: scoreDeterministic (eval.go) routes any
//     "rag_grounded" item with len(it.Answer) > 0 to scoring.ScoreRAGMultiHop,
//     never to the single-hop hit@1 scoring.ScoreRAG. h3/h4/h5/h6 all carry a
//     non-empty Answer in arenas/live/_shared/rag_questions.jsonl, so they all
//     reach the multi-hop scorer. See TestScoreDeterministic_MultiHopDispatch.
//
//  2. ScoreRAGMultiHop's Pass rule intentionally requires BOTH sub-signals to
//     be perfect: recall over ALL gold docs AND a complete answer-key match
//     (scoring.go ScoreRAGMultiHop godoc). A calibration row where the answer
//     text is fully correct (containment 1.00) but the item still fails is
//     exactly what happens when retrieval missed one of the two gold docs
//     (recall < 1.0) — the scorer is correctly refusing to credit a
//     right-looking answer that was not actually grounded in the full
//     evidentiary basis the task designates. See
//     TestScoreDeterministic_MultiHopPartialRecallFailsDespitePerfectAnswer,
//     which reproduces the h4 scenario byte-for-byte with the real task data.
//
// rec.RetrievalHit is set to rec.Det.Pass regardless of which scorer ran
// (eval.go scoreRecord), so the "hit@1 ✗" LABEL in results.go's rendering is
// generic across single-hop and multi-hop items — that rendering choice lives
// in results.go, which this task does not own, and is a separate (cosmetic,
// not scoring-logic) concern from what is investigated here.

// TestScoreDeterministic_MultiHopDispatch proves every hard-tier multi-hop rag
// item (identified by carrying an answer key) reaches ScoreRAGMultiHop, and a
// single-hop item (no answer key) still reaches the plain hit@1 ScoreRAG.
func TestScoreDeterministic_MultiHopDispatch(t *testing.T) {
	// Single-hop grounded question: no answer key -> hit@1 (ScoreRAG).
	singleHop := workItem{
		Kind:           "rag_grounded",
		SupportingDocs: []string{"08-blue-guarantee.md"},
		// Answer intentionally empty/nil.
	}
	singleHopRec := &record{Out: adapterOutput{
		Reply:     "More than 12 minutes late.",
		Retrieved: []retrievedDoc{{DocID: "08-blue-guarantee.md"}},
	}}
	sc := scoreDeterministic(singleHopRec, singleHop)
	if sc.Detail == "" || !sc.Pass {
		t.Fatalf("single-hop hit@1 should pass on a top-ranked gold doc, got %+v", sc)
	}

	// Multi-hop grounded question (h4 shape): answer key present -> must NOT
	// use hit@1 semantics. We prove this by giving it a retrieved set where
	// the TOP doc is NOT a gold doc at all (hit@1 would fail immediately) but
	// BOTH gold docs are present somewhere in the retrieved set and the reply
	// is a complete answer — ScoreRAGMultiHop must pass this, which ScoreRAG
	// (hit@1) never would.
	multiHop := workItem{
		Kind:           "rag_grounded",
		SupportingDocs: []string{"08-blue-guarantee.md", "04-halcyon-pass.md"},
		Answer:         []string{"Halcyon 1 December 1 March"},
	}
	multiHopRec := &record{Out: adapterOutput{
		Reply: "Halcyon Pass; the closure runs from 1 December to 1 March each year.",
		Retrieved: []retrievedDoc{
			{DocID: "99-unrelated.md"}, // top doc is NOT gold — would fail hit@1
			{DocID: "08-blue-guarantee.md"},
			{DocID: "04-halcyon-pass.md"},
		},
	}}
	got := scoreDeterministic(multiHopRec, multiHop)
	if !got.Pass {
		t.Fatalf("multi-hop item with full recall + complete answer must pass despite a non-gold top doc (proves multi-hop dispatch, not hit@1), got %+v", got)
	}

	// Sanity: if this same input were scored with plain hit@1 semantics it
	// would fail (top doc is not gold), confirming the two scorers disagree
	// and therefore that routing to the right one matters.
	hitAt1 := scoring.ScoreRAG("99-unrelated.md", multiHop.SupportingDocs, multiHopRec.Out.Reply, false)
	if hitAt1.Pass {
		t.Fatalf("test setup invalid: hit@1 was expected to fail on a non-gold top doc, got %+v", hitAt1)
	}
}

// TestScoreDeterministic_MultiHopPartialRecallFailsDespitePerfectAnswer
// reproduces the exact h4 calibration row: the answer-key containment is
// 1.00 (the composed answer is fully correct) but only ONE of the two gold
// docs was retrieved, so the item must still FAIL overall. This is the
// documented, intentional behavior of ScoreRAGMultiHop (see scoring.go) — a
// framework must not be credited for a right-sounding answer it did not
// actually ground in the full designated evidence set.
func TestScoreDeterministic_MultiHopPartialRecallFailsDespitePerfectAnswer(t *testing.T) {
	it := workItem{
		Kind:           "rag_grounded",
		SupportingDocs: []string{"08-blue-guarantee.md", "04-halcyon-pass.md"},
		Answer:         []string{"Halcyon 1 December 1 March"},
	}
	rec := &record{Out: adapterOutput{
		Reply: "Halcyon Pass; the closure runs from 1 December to 1 March each year.",
		// Only ONE of the two designated gold docs retrieved.
		Retrieved: []retrievedDoc{{DocID: "08-blue-guarantee.md"}},
	}}
	got := scoreDeterministic(rec, it)
	if got.Pass {
		t.Fatalf("partial gold-doc recall must NOT pass even with a perfect answer, got %+v", got)
	}
	if got.Value != 0.75 { // recall 0.5 (1/2 gold docs) + answer 1.0, averaged
		t.Fatalf("want the visible partial value 0.75 (recall 0.5, answer 1.0), got %v (%s)", got.Value, got.Detail)
	}

	// rec.RetrievalHit mirrors rec.Det.Pass regardless of scorer (eval.go
	// scoreRecord) — confirm the mode-signal source is exactly this Pass bit.
	rec2 := &record{Out: rec.Out}
	rec2.Det = scoreDeterministic(rec2, it)
	rec2.RetrievalHit = rec2.Det.Pass
	if rec2.RetrievalHit {
		t.Fatalf("RetrievalHit should mirror the failed multi-hop Pass, got true")
	}
}

// ---- DEFECT 2 regression: mem-h01-overwrite's "best key" was empty --------

// TestScoreDeterministic_QAZeroMatchNamesTheTriedKey reproduces the
// mem-h01-overwrite calibration finding: a completely wrong reply against a
// well-formed single answer key ("9A") must name the key it tried in the
// failure Detail, not render an empty "best key \"\"" (which read as a
// malformed task rather than a wrong-answer failure).
func TestScoreDeterministic_QAZeroMatchNamesTheTriedKey(t *testing.T) {
	it := workItem{Kind: "qa", Answer: []string{"9A"}}
	rec := &record{Out: adapterOutput{Reply: "Your seat has been changed to a different one."}}
	got := scoreDeterministic(rec, it)
	if got.Pass {
		t.Fatalf("a reply that never mentions the answer key must fail, got %+v", got)
	}
	want := `answer-key containment 0.00 (no key matched; tried "9A")`
	if got.Detail != want {
		t.Fatalf("want Detail %q, got %q", want, got.Detail)
	}
}
