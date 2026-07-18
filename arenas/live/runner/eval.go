package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// scoreRecord fills a successful record's mode-specific correctness signals and,
// where a reference answer exists, its LLM-judge scores. It only runs on records
// that did not fail (guaranteed by the caller).
func scoreRecord(rec *record, it workItem, judge *judgeClient, budget *callCounter) {
	switch it.Kind {
	case "rag_grounded":
		rec.RetrievalHit = retrievalHit(rec.Out.Retrieved, it.SupportingDocs)
		judgeInto(rec, it, judge, budget)
	case "rag_trap":
		rec.TrapRefused = looksLikeRefusal(rec.Out.Reply)
		// The trap "reference" is meta-text describing correct-refusal behavior,
		// so a factual judge pass is not meaningful here — the metric is refusal.
	case "tools":
		rec.ToolExecuted = toolExecuted(rec.Out.ToolCalls)
		rec.AnswerCorrect = numberPresent(rec.Out.Reply, it.ExpectedNumber)
	case "structured":
		rec.StructuredOK = rec.Out.Structured != nil
		judgeInto(rec, it, judge, budget)
	case "stream":
		rec.TTFTMs = rec.Out.TTFTMs
		judgeInto(rec, it, judge, budget)
	default: // qa, memory
		judgeInto(rec, it, judge, budget)
	}
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

// retrievalHit is hit@1: the top retrieved doc is one of the gold docs.
func retrievalHit(docs []retrievedDoc, gold []string) bool {
	if len(docs) == 0 || len(gold) == 0 {
		return false
	}
	top := strings.TrimSpace(docs[0].DocID)
	for _, g := range gold {
		if strings.EqualFold(strings.TrimSpace(g), top) {
			return true
		}
	}
	return false
}

var refusalRe = regexp.MustCompile(`(?i)(don't know|do not know|not available|no information|not in the (source|corpus|context|documents)|isn't in|is not in|cannot find|can't find|couldn't find|unable to|does not (state|mention|contain|say|specify|provide)|doesn't (state|mention|contain|say|specify|provide)|no mention|not mentioned|not provided|not specified|not stated|no such|not found|i (do not|don't) have)`)

func looksLikeRefusal(reply string) bool {
	return refusalRe.MatchString(reply)
}

func toolExecuted(calls []toolCall) bool {
	for _, c := range calls {
		if strings.TrimSpace(c.Result) != "" {
			return true
		}
	}
	return false
}

// numberPresent reports whether the expected number appears in the reply, after
// stripping thousands separators from both sides (so "4,183" matches "4183").
func numberPresent(reply, expected string) bool {
	if expected == "" {
		return false
	}
	norm := func(s string) string { return strings.ReplaceAll(s, ",", "") }
	return strings.Contains(norm(reply), norm(expected))
}

// ---- LLM-as-judge (agnes, /chat/completions — NOT the Responses API) --------

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
