package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// evalTenant / evalUser are the fixed isolation identity every turn and every
// knowledge seed in this harness is scoped to. The eval runs the agent the way
// production serves it — through a real tenant/user scope — so the
// constitutional isolation gate holds during evaluation, not just in the
// request path.
const (
	evalTenant = "eval"
	evalUser   = "answer-quality"
)

// evalRatePerSec / evalBurst lift the per-tenant token-bucket well above the
// serving default (10/s, burst 30) FOR THIS TRUSTED LOCAL HARNESS ONLY, so a
// bounded batch of sequential eval turns is never throttled by the quota gate.
// It does not change the product default — it is a per-App option on the
// harness's own App instance.
const (
	evalRatePerSec = 100000
	evalBurst      = 100000
)

// questionResult is one question's end-to-end outcome: the produced answer, the
// judge scores, latency, and the token spend split into the turn (serving) side
// and the judge side.
type questionResult struct {
	ID     string
	Prompt string
	Answer string
	Stub   bool

	Scores answerScores

	LatencyMS   int64
	TurnInTok   int64
	TurnOutTok  int64
	JudgeInTok  int64
	JudgeOutTok int64

	Err string
}

// runResult is the whole run: per-question results, the resolved providers and
// whether each is real, the cumulative token spend, and (when a token budget is
// hit) the abort marker.
type runResult struct {
	SetName string
	Results []questionResult

	ServingProvider string
	ServingReal     bool
	JudgeProvider   string
	JudgeReal       bool

	TurnInTok   int64
	TurnOutTok  int64
	JudgeInTok  int64
	JudgeOutTok int64

	InPricePer1M  float64
	OutPricePer1M float64

	Aborted     bool
	AbortReason string
}

// runEval executes the full pipeline for every question: it boots one shared
// App (provider-agnostic serving client via llm.FromEnv, metered for token
// spend), seeds every question's context_docs into the REAL memory tier, then
// for each question runs App.Turn (full parity with POST /v1/.../chat — quota
// gate, guardrails, gateway model selection, memory hydration, fail-closed
// reflection, cost accounting) and scores the answer with the provider-agnostic
// LLM-as-a-judge. maxTokens > 0 aborts the loop as soon as cumulative spend
// crosses it — the runaway-spend guard.
func runEval(ctx context.Context, questions []question, setName string, inPrice, outPrice float64, maxTokens int64) (*runResult, error) {
	reg := llm.RegistryFromEnv()
	servingProvider := reg.DefaultName()
	servingMeter := newMeterClient(reg.Default())

	jc := eval.JudgeClientFromEnv()
	judgeMeter := newMeterClient(jc.Client)
	sc, err := newScorer(judgeMeter)
	if err != nil {
		return nil, err
	}

	app := aisotropy.New(
		aisotropy.WithLLM(servingMeter),
		aisotropy.WithRateLimit(evalRatePerSec, evalBurst),
	)
	defer func() { _ = app.Close() }()

	if err := seedKnowledge(ctx, app, questions); err != nil {
		return nil, err
	}

	rr := &runResult{
		SetName:         setName,
		ServingProvider: servingProvider,
		ServingReal:     servingProvider != providerStubName,
		JudgeProvider:   jc.Provider,
		JudgeReal:       jc.Real,
		InPricePer1M:    inPrice,
		OutPricePer1M:   outPrice,
		Results:         make([]questionResult, 0, len(questions)),
	}

	var prevTurnIn, prevTurnOut, prevJudgeIn, prevJudgeOut int64
	for _, q := range questions {
		if maxTokens > 0 {
			ti, to, _ := servingMeter.snapshot()
			ji, jo, _ := judgeMeter.snapshot()
			if ti+to+ji+jo >= maxTokens {
				rr.Aborted = true
				rr.AbortReason = fmt.Sprintf("token budget %d reached before question %q (guard AISOTROPY_EVAL_MAX_TOKENS)", maxTokens, q.ID)
				break
			}
		}

		res := questionResult{ID: q.ID, Prompt: q.Prompt}

		t0 := time.Now()
		out, terr := app.Turn(ctx, aisotropy.ChatInput{
			TenantID:  evalTenant,
			UserID:    evalUser,
			Prompt:    q.Prompt,
			SessionID: q.ID, // one conversation per question: no cross-question bleed
		})
		res.LatencyMS = time.Since(t0).Milliseconds()

		ti, to, _ := servingMeter.snapshot()
		res.TurnInTok, res.TurnOutTok = ti-prevTurnIn, to-prevTurnOut
		prevTurnIn, prevTurnOut = ti, to

		if terr != nil {
			res.Err = terr.Error()
			rr.Results = append(rr.Results, res)
			continue
		}
		res.Answer = out.Reply
		res.Stub = out.Stub

		scores, serr := sc.score(ctx, q, out.Reply)
		ji, jo, _ := judgeMeter.snapshot()
		res.JudgeInTok, res.JudgeOutTok = ji-prevJudgeIn, jo-prevJudgeOut
		prevJudgeIn, prevJudgeOut = ji, jo
		if serr != nil {
			res.Err = serr.Error()
			rr.Results = append(rr.Results, res)
			continue
		}
		res.Scores = scores
		rr.Results = append(rr.Results, res)
	}

	rr.TurnInTok, rr.TurnOutTok, _ = servingMeter.snapshot()
	rr.JudgeInTok, rr.JudgeOutTok, _ = judgeMeter.snapshot()
	return rr, nil
}

// providerStubName is the registry name of the keyless fallback provider,
// mirrored locally (llm does not export it) to detect a stub serving client.
const providerStubName = "stub"

// seedKnowledge ingests every question's context_docs into the App's memory
// tier under the eval scope, deduplicated by document ID, so the turn's real
// RAG hydration can retrieve them. Questions with no context_docs contribute
// nothing; a run with zero docs seeds nothing and still works (the agent then
// answers from its own knowledge, and faithfulness is simply not scored).
func seedKnowledge(ctx context.Context, app *aisotropy.App, questions []question) error {
	seen := make(map[string]bool)
	docs := make([]aisotropy.Document, 0)
	for _, q := range questions {
		for _, d := range q.Docs {
			if d.Text == "" || seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			docs = append(docs, aisotropy.Document{ID: d.ID, Content: d.Text, Source: d.Source})
		}
	}
	if len(docs) == 0 {
		return nil
	}
	if _, err := app.IngestKnowledge(ctx, evalTenant, evalUser, docs...); err != nil {
		return fmt.Errorf("answer-quality: seed knowledge: %w", err)
	}
	return nil
}
