package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"aisotropy-live-runner/scoring"
)

// scoreRecord fills a successful record's scores. The DETERMINISTIC score
// (rec.Det, arenas/live/runner/scoring) is the PRIMARY signal and is ALWAYS
// computed — pure, reproducible, no LLM/network/randomness — regardless of
// whether the LLM-judge is enabled. The LLM-judge scores (Correctness/
// Helpfulness/Faithfulness) are SECONDARY and only computed when judge!=nil
// (opt-in via -llm-judge; see main.go and
// docs/architecture/arena-deterministic-judging.md). It only runs on
// records that did not fail (guaranteed by the caller).
func scoreRecord(rec *record, it workItem, judge *judgeClient, budget *callCounter) {
	rec.Det = scoreDeterministic(rec, it)

	switch it.Kind {
	case "rag_grounded":
		rec.RetrievalHit = rec.Det.Pass
		judgeInto(rec, it, judge, budget)
	case "rag_trap":
		rec.TrapRefused = rec.Det.Pass
		// The trap "reference" is meta-text describing correct-refusal behavior,
		// so a factual judge pass is not meaningful here — the metric is refusal.
	case "tools":
		rec.ToolExecuted = scoring.ToolExecuted(toScoringCalls(rec.Out.ToolCalls))
		rec.AnswerCorrect = rec.Det.Pass
	case "structured":
		rec.StructuredOK = rec.Det.Pass
		judgeInto(rec, it, judge, budget)
	case "stream":
		rec.TTFTMs = rec.Out.TTFTMs
		judgeInto(rec, it, judge, budget)
	default: // qa, memory
		judgeInto(rec, it, judge, budget)
	}
}

// scoreDeterministic computes the PRIMARY, deterministic per-dimension score
// by dispatching to arenas/live/runner/scoring — the pure, reproducible,
// LLM-free scorer. This is the single place the runner maps its own
// record/workItem shapes onto the scoring package's plain input types; the
// scoring package itself has no knowledge of the runner's types.
func scoreDeterministic(rec *record, it workItem) scoring.Score {
	switch it.Kind {
	case "rag_grounded":
		return scoring.ScoreRAG(topDocID(rec.Out.Retrieved), it.SupportingDocs, rec.Out.Reply, false)
	case "rag_trap":
		return scoring.ScoreRAG("", nil, rec.Out.Reply, true)
	case "tools":
		return scoring.ScoreToolsNumeric(toScoringCalls(rec.Out.ToolCalls), rec.Out.Reply, it.ExpectedNumber)
	case "structured":
		return scoring.ScoreStructured(rec.Out.Structured)
	case "stream":
		return scoring.ScoreStream(rec.Out.TTFTMs)
	case "memory":
		return scoring.ScoreMemory(rec.Out.Reply, it.Reference, it.Answer)
	default: // "qa"
		return scoring.ScoreQA(rec.Out.Reply, it.Reference, it.Answer)
	}
}

// toScoringCalls converts the runner's own toolCall shape into the scoring
// package's dependency-free ToolCall view.
func toScoringCalls(calls []toolCall) []scoring.ToolCall {
	out := make([]scoring.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = scoring.ToolCall{Name: c.Name, Arguments: c.Arguments, Executed: strings.TrimSpace(c.Result) != ""}
	}
	return out
}

// topDocID returns the top retrieved doc's ID (hit@1 candidate), or "" when
// nothing was retrieved.
func topDocID(docs []retrievedDoc) string {
	if len(docs) == 0 {
		return ""
	}
	return docs[0].DocID
}

func judgeInto(rec *record, it workItem, judge *judgeClient, budget *callCounter) {
	if judge == nil || it.Reference == "" {
		return
	}
	answer := rec.Out.Reply
	if answer == "" && rec.Out.Structured != nil {
		answer = fmt.Sprintf("%v", rec.Out.Structured)
	}
	sc, err := judge.score(it.Question, it.Reference, answer)
	budget.add(1)
	if err != nil {
		rec.JudgeNote = "judge error: " + err.Error()
		return
	}
	rec.Correctness = sc.Correctness
	rec.Helpfulness = sc.Helpfulness
	rec.Faithfulness = sc.Faithfulness
	rec.JudgeNote = sc.Rationale
}

// ---- LLM-as-judge (agnes, /chat/completions — NOT the Responses API) --------
//
// SECONDARY, opt-in (-llm-judge), caveated. The deterministic scorer above
// (arenas/live/runner/scoring) is the arena's PRIMARY score; this judge
// exists only as an additional, clearly-labeled directional signal — see
// docs/architecture/arena-deterministic-judging.md.

type judgeClient struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

func newJudgeClient() (*judgeClient, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &judgeClient{apiKey: key, model: modelName(), baseURL: base, http: &http.Client{Timeout: 90 * time.Second}}, nil
}

type judgeScore struct {
	Correctness  int    `json:"correctness"`
	Helpfulness  int    `json:"helpfulness"`
	Faithfulness int    `json:"faithfulness"`
	Rationale    string `json:"rationale"`
}

func (j *judgeClient) score(question, reference, candidate string) (judgeScore, error) {
	sys := "You are a strict, impartial grader of AI assistant answers. " +
		"Score the CANDIDATE answer against the REFERENCE on three axes, each an integer 1-5:\n" +
		"- correctness: is the candidate factually correct and consistent with the reference?\n" +
		"- helpfulness: does it actually answer what was asked, clearly and completely?\n" +
		"- faithfulness: does it stay grounded (no invented facts, no contradiction of the reference)?\n" +
		"Reply with ONLY a JSON object: {\"correctness\":N,\"helpfulness\":N,\"faithfulness\":N,\"rationale\":\"one short sentence\"}."
	user := fmt.Sprintf("QUESTION:\n%s\n\nREFERENCE answer:\n%s\n\nCANDIDATE answer:\n%s", question, reference, candidate)

	body, _ := json.Marshal(map[string]any{
		"model":       j.model,
		"max_tokens":  300,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, j.baseURL+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+j.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := j.http.Do(req)
	if err != nil {
		return judgeScore{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return judgeScore{}, fmt.Errorf("judge status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return judgeScore{}, err
	}
	if len(out.Choices) == 0 {
		return judgeScore{}, fmt.Errorf("judge returned no choices")
	}
	return parseJudgeScore(out.Choices[0].Message.Content)
}

func parseJudgeScore(content string) (judgeScore, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return judgeScore{}, fmt.Errorf("no JSON object in judge reply: %s", tail(content, 120))
	}
	var sc judgeScore
	if err := json.Unmarshal([]byte(content[start:end+1]), &sc); err != nil {
		return judgeScore{}, fmt.Errorf("decode judge JSON: %w", err)
	}
	return sc, nil
}
