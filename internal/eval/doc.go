// Package eval is Aisotropy's product-evaluation harness — the first brick of
// the evaluation pillar Chip Huyen's "AI Engineering" (Ch. 3–4) puts before
// everything else: you cannot improve, and must not ship, what you cannot
// measure. It maps that book's three-step evaluation pipeline directly into
// code, deliberately as a clean, dependency-free foundation (stdlib + existing
// internal packages, no new module) rather than a platform.
//
// # The three steps
//
//  1. Evaluate each COMPONENT, not just the final answer. A RAG-style agent is
//     a chain (retriever -> context assembly -> generation), and an end-to-end
//     score hides WHICH link failed. So this package evaluates the retriever
//     on its own terms (retrieval.go, wired to aisotropy's REAL semantic.Store
//     via EvalRetrieval — not a toy) with information-retrieval metrics
//     (metrics.go: PrecisionAtK, RecallAtK, F1, ReciprocalRank/MRR), AND the
//     generation on its own terms (judge.go, per-criterion scoring).
//
//  2. Have an evaluation GUIDE: explicit criteria, a scored rubric per
//     criterion, and labeled examples (guide.go: Criterion, Rubric,
//     RubricExample, EvalSample, EvalCase). "Good" is defined up front, with
//     illustrations, so a score is reproducible and reviewable — not an ad-hoc
//     vibe. The book's own headline criteria ship as first-class definitions:
//     Relevance, Faithfulness (grounding / factuality to the provided
//     context), and Safety (toxicity).
//
//  3. Decide HOW to evaluate each criterion — the three methods the book
//     enumerates (judge.go):
//     - LLM-as-a-judge: LLMJudge scores an open-ended criterion by asking a
//     model for a TYPED verdict. It reuses aisotropy's struct-as-contract
//     reflection loop (internal/llm.GollemReflect over the same Client the
//     product serves with), so the judge's output is a validated struct, not
//     free text that might not parse.
//     - Code / exact function: CodeJudge runs a deterministic Go func — e.g.
//     JSON-validity or a keyword/regex check — for criteria that need no
//     model at all. Cheap, exact, and the right tool when the criterion is
//     mechanical.
//     - Human: out of scope for automated runs, but EvalCase carries labeled
//     expected scores so a human-graded gold set slots into the same shape.
//
// # Running a suite
//
// RunSuite (guide.go) ties the guide to the methods: it scores every EvalCase
// with every Judge, then aggregates the mean score per criterion and a
// pass/fail count against a threshold. EvalRetrieval does the analogous job
// for the retriever. Both are pure orchestration over deterministic inputs:
// the tests drive the in-process semantic.Store with a HashEmbedder and the
// llm.StubClient, so the whole harness runs in CI with no external service and
// no ANTHROPIC_API_KEY.
//
// # Isolation
//
// Retrieval evaluation runs through a real semantic.Scope, so every store read
// is tenant/user-scoped exactly like production traffic (the constitutional
// isolation gate, .claude/rules/constitutional-gates.md). The harness never
// widens a store's filter to "see everything"; a case is evaluated within one
// scope, the way the product would serve it.
package eval
