package main

import (
	"strings"
	"testing"
)

func TestToFive(t *testing.T) {
	tests := []struct {
		norm float64
		want float64
	}{
		{0, 1},
		{1, 5},
		{0.5, 3},
	}
	for _, tt := range tests {
		if got := toFive(tt.norm); got != tt.want {
			t.Errorf("toFive(%v) = %v, want %v", tt.norm, got, tt.want)
		}
	}
}

func TestEstimateFor(t *testing.T) {
	questions := []question{
		{ID: "grounded", Prompt: "q1", Context: "some grounding context"},
		{ID: "open", Prompt: "q2"},
	}
	e := estimateFor(questions)

	if e.Questions != 2 {
		t.Errorf("Questions = %d, want 2", e.Questions)
	}
	if e.TurnCalls != 2 {
		t.Errorf("TurnCalls = %d, want 2", e.TurnCalls)
	}
	// grounded -> 3 judge calls, open -> 2 judge calls.
	if e.JudgeCalls != 5 {
		t.Errorf("JudgeCalls = %d, want 5", e.JudgeCalls)
	}
	if got := e.totalCalls(); got != 7 {
		t.Errorf("totalCalls() = %d, want 7", got)
	}
	wantTokens := 7 * (e.AssumedInPerCall + e.AssumedOutPerCall)
	if got := e.roughTokens(); got != wantTokens {
		t.Errorf("roughTokens() = %d, want %d", got, wantTokens)
	}
}

func TestEstimateForEmptySet(t *testing.T) {
	e := estimateFor(nil)
	if e.Questions != 0 || e.TurnCalls != 0 || e.JudgeCalls != 0 {
		t.Fatalf("estimateFor(nil) = %+v, want all zero", e)
	}
	if e.totalCalls() != 0 || e.roughTokens() != 0 {
		t.Fatalf("totalCalls/roughTokens on an empty estimate should be zero")
	}
}

func TestRenderEstimateRealProviders(t *testing.T) {
	e := estimateInfo{Questions: 3, TurnCalls: 3, JudgeCalls: 6, AssumedInPerCall: 400, AssumedOutPerCall: 150}
	out := renderEstimate(e, true, true)

	if !strings.Contains(out, "PRE-RUN SPEND ESTIMATE") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "questions          : 3") {
		t.Errorf("missing question count: %q", out)
	}
	if strings.Contains(out, "STUB run") {
		t.Error("real providers must not print the STUB note")
	}
}

func TestRenderEstimateStubNoteWhenEitherProviderIsStub(t *testing.T) {
	e := estimateInfo{Questions: 1, TurnCalls: 1, JudgeCalls: 2}

	for _, tt := range []struct {
		name                   string
		servingReal, judgeReal bool
	}{
		{"both-stub", false, false},
		{"serving-stub-only", false, true},
		{"judge-stub-only", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := renderEstimate(e, tt.servingReal, tt.judgeReal)
			if !strings.Contains(out, "STUB run") {
				t.Errorf("expected the STUB note when servingReal=%v judgeReal=%v; got:\n%s", tt.servingReal, tt.judgeReal, out)
			}
		})
	}
}

func TestSummarizeAggregatesScoresAndFlags(t *testing.T) {
	rr := &runResult{
		Results: []questionResult{
			{
				ID:        "grounded-good",
				LatencyMS: 100,
				Scores: answerScores{
					correctness: 1.0, helpfulness: 1.0,
					faithfulness: 0.9, faithfulnessScored: true,
					refused: false, hallucinated: false,
				},
			},
			{
				ID:        "grounded-hallucinated",
				LatencyMS: 200,
				Scores: answerScores{
					correctness: 0.5, helpfulness: 0.5,
					faithfulness: 0.1, faithfulnessScored: true,
					refused: true, hallucinated: true,
				},
			},
			{
				ID:        "ungrounded",
				LatencyMS: 300,
				Scores:    answerScores{correctness: 0.0, helpfulness: 0.0},
			},
			{ID: "errored", Err: "boom"},
		},
	}

	a := summarize(rr)

	if a.scored != 3 {
		t.Errorf("scored = %d, want 3", a.scored)
	}
	if a.errored != 1 {
		t.Errorf("errored = %d, want 1", a.errored)
	}
	if a.faithfulN != 2 {
		t.Errorf("faithfulN = %d, want 2", a.faithfulN)
	}
	if a.refusals != 1 {
		t.Errorf("refusals = %d, want 1", a.refusals)
	}
	if a.hallucinations != 1 {
		t.Errorf("hallucinations = %d, want 1", a.hallucinations)
	}
	wantLatency := (100.0 + 200.0 + 300.0) / 3.0
	if a.meanLatencyMS != wantLatency {
		t.Errorf("meanLatencyMS = %v, want %v", a.meanLatencyMS, wantLatency)
	}
	// meanCorrectness = toFive(mean of 1.0, 0.5, 0.0) = toFive(0.5) = 3.
	if a.meanCorrectness != 3 {
		t.Errorf("meanCorrectness = %v, want 3", a.meanCorrectness)
	}
}

func TestSummarizeNoScoredQuestionsLeavesMeansZero(t *testing.T) {
	rr := &runResult{Results: []questionResult{{ID: "e1", Err: "boom"}}}
	a := summarize(rr)
	if a.scored != 0 || a.meanCorrectness != 0 || a.meanHelpfulness != 0 || a.meanLatencyMS != 0 {
		t.Fatalf("summarize with no scored questions = %+v, want all means zero", a)
	}
	if a.faithfulN != 0 || a.meanFaithful != 0 {
		t.Fatalf("expected zero faithfulness aggregate, got %+v", a)
	}
}

func TestRenderIncludesStubBannerAbortReasonAndPerQuestionRows(t *testing.T) {
	rr := &runResult{
		SetName:         "unit-set",
		ServingProvider: "stub",
		ServingReal:     false,
		JudgeProvider:   "stub",
		JudgeReal:       false,
		Aborted:         true,
		AbortReason:     "token budget reached",
		Results: []questionResult{
			{
				ID: "grounded", TurnInTok: 10, TurnOutTok: 5, JudgeInTok: 20, JudgeOutTok: 8, LatencyMS: 42,
				Scores: answerScores{correctness: 1, helpfulness: 1, faithfulness: 0.9, faithfulnessScored: true},
			},
			{ID: "broken-question-with-a-very-long-id", Err: "judge failed"},
		},
	}

	out := render(rr)

	for _, want := range []string{
		"END-TO-END ANSWER-QUALITY EVAL",
		"unit-set",
		"STUB RUN",
		"RUN ABORTED: token budget reached",
		"grounded",
		"ERROR: judge failed",
		"GENERIC vs DOMAIN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render() output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderRealProvidersOmitStubBanner(t *testing.T) {
	rr := &runResult{
		SetName: "real-set", ServingProvider: "openrouter", ServingReal: true,
		JudgeProvider: "openrouter", JudgeReal: true,
		Results: []questionResult{{ID: "q1", Scores: answerScores{correctness: 1, helpfulness: 1}}},
	}
	out := render(rr)
	if strings.Contains(out, "STUB RUN") {
		t.Error("a real-provider run must not print the STUB RUN banner")
	}
	if !strings.Contains(out, "faithfulness     : n/a") {
		t.Errorf("expected the n/a faithfulness line when no question carried grounding: %s", out)
	}
}

func TestRenderCostBranches(t *testing.T) {
	noPricing := &runResult{}
	if got := renderCost(noPricing, 100, 50); !strings.Contains(got, "n/a") {
		t.Errorf("renderCost with no pricing = %q, want an n/a note", got)
	}

	priced := &runResult{InPricePer1M: 3, OutPricePer1M: 15}
	got := renderCost(priced, 1_000_000, 1_000_000)
	if !strings.Contains(got, "est. cost") || !strings.Contains(got, "$") {
		t.Errorf("renderCost with pricing = %q, want a dollar estimate", got)
	}
}

func TestRealTag(t *testing.T) {
	if got := realTag(true); !strings.Contains(got, "REAL") {
		t.Errorf("realTag(true) = %q, want it to mention REAL", got)
	}
	if got := realTag(false); !strings.Contains(got, "STUB") {
		t.Errorf("realTag(false) = %q, want it to mention STUB", got)
	}
}

func TestPct(t *testing.T) {
	if got := pct(0, 0); got != "n/a" {
		t.Errorf("pct(0,0) = %q, want n/a", got)
	}
	if got := pct(1, 4); got != "1/4 (25%)" {
		t.Errorf("pct(1,4) = %q, want \"1/4 (25%%)\"", got)
	}
}

func TestBoolMark(t *testing.T) {
	if boolMark(true) != "yes" {
		t.Errorf("boolMark(true) != yes")
	}
	if boolMark(false) != "no" {
		t.Errorf("boolMark(false) != no")
	}
}

func TestTrunc(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"shorter-than-n", "abc", 10, "abc"},
		{"exactly-n", "abcde", 5, "abcde"},
		{"truncated", "abcdefgh", 5, "abcd…"},
		{"n-is-one", "abcdefgh", 1, "a"},
		{"n-is-zero", "abcdefgh", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trunc(tt.s, tt.n); got != tt.want {
				t.Errorf("trunc(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}
