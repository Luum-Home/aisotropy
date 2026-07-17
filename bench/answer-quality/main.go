// Command answer-quality is the END-TO-END answer-quality eval for aisotropy: it
// runs real questions through the FULL turn pipeline (embedding aisotropy.App
// and calling App.Turn — same isolation, quota gate, guardrails, gateway model
// selection, memory hydration, fail-closed reflection, and cost accounting as
// POST /v1/.../chat) with the REAL model resolved provider-agnostically from the
// environment, captures each answer, and then grades it with an LLM-as-a-judge
// on correctness, faithfulness-to-context, and helpfulness — plus a model-free
// refusal rate, a hallucination flag, latency, and measured TOKEN SPEND.
//
// It answers the launch-readiness question retrieval eval (bench/rag-quality,
// ADR-0058) does NOT: retrieval quality is measured, but "does the AGENT
// actually answer well with a REAL model" was unmeasured — every prior test used
// the keyless stub. This binary measures it.
//
// # Provider-agnostic (ADR-0009)
//
// The serving model AND the judge are both selected via llm.RegistryFromEnv /
// eval.JudgeClientFromEnv precedence: OPENROUTER_API_KEY > OPENAI_API_KEY >
// ANTHROPIC_API_KEY > stub. There is NO hardcoded ANTHROPIC gate. With no
// provider key everything runs on the deterministic stub and the report is
// LOUDLY labeled "STUB RUN — proves wiring, NOT real answer quality."
//
// # Key handling
//
// The operator exports THEIR OWN provider key into the environment before
// running; the harness reads it from the environment and NEVER logs it. This
// process never asks for, stores, or prints a key.
//
// # Usage
//
//	# Real run on the operator's own key + model (bounded to 20 questions):
//	export OPENROUTER_API_KEY=…  OPENROUTER_MODEL=…
//	go run ./bench/answer-quality -n 20
//
//	# Point it at the operator's own domain Q&A (the ONLY real measure):
//	go run ./bench/answer-quality -data ./my-domain.json -n 50
//
//	# No key: stub smoke that proves the pipeline + judge wiring end-to-end:
//	go run ./bench/answer-quality -n 5
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

func main() {
	var (
		n        = flag.Int("n", 20, "number of questions to run (bounded by -cap)")
		cap_     = flag.Int("cap", 200, "hard maximum for -n, so a run can never accidentally balloon")
		dataPath = flag.String("data", "", "path to a question set JSON (default: the bundled generic smoke set)")
		outPath  = flag.String("out", defaultOutPath(), "path to write the results report")
		quiet    = flag.Bool("quiet", false, "suppress embedder/runtime slog noise")
	)
	flag.Parse()

	if *quiet {
		slog.SetLogLoggerLevel(slog.LevelError)
	}

	if err := run(context.Background(), *n, *cap_, *dataPath, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "answer-quality: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, n, cap_ int, dataPath, outPath string) error {
	if n <= 0 {
		return fmt.Errorf("-n must be positive (got %d)", n)
	}
	if n > cap_ {
		return fmt.Errorf("-n %d exceeds the hard cap -cap %d; raise -cap explicitly if you really mean to run that many (bounded-spend guard)", n, cap_)
	}

	path := dataPath
	if path == "" {
		path = defaultDataPath()
	}
	setName, questions, err := loadQuestionsFile(path)
	if err != nil {
		return err
	}
	if n < len(questions) {
		questions = questions[:n]
	}

	inPrice := floatFromEnv("AISOTROPY_EVAL_IN_PRICE_PER_1M")
	outPrice := floatFromEnv("AISOTROPY_EVAL_OUT_PRICE_PER_1M")
	maxTokens := int64FromEnv("AISOTROPY_EVAL_MAX_TOKENS")

	// Pre-run estimate (rough) + honest stub labeling, printed BEFORE spending.
	servingReal := llm.RegistryFromEnv().DefaultName() != "stub"
	judgeReal := eval.JudgeClientFromEnv().Real
	est := estimateFor(questions)
	fmt.Print(renderEstimate(est, servingReal, judgeReal))
	fmt.Println()

	rr, err := runEval(ctx, questions, setName, inPrice, outPrice, maxTokens)
	if err != nil {
		return err
	}

	report := render(rr)
	fmt.Print(report)

	if outPath != "" {
		if err := os.WriteFile(outPath, []byte(report), 0o644); err != nil {
			return fmt.Errorf("write report %q: %w", outPath, err)
		}
		fmt.Printf("\nwrote %s\n", outPath)
	}
	return nil
}

// defaultDataPath resolves the bundled generic smoke set relative to this
// source file, so `go run ./bench/answer-quality` works from any cwd.
func defaultDataPath() string {
	return filepath.Join(sourceDir(), "testdata", "generic.json")
}

// defaultOutPath writes results.txt next to this source file.
func defaultOutPath() string {
	return filepath.Join(sourceDir(), "results.txt")
}

// sourceDir returns the directory of this source file (runtime.Caller), the
// same idiom bench/rag-quality uses to locate its testdata regardless of cwd.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}

// floatFromEnv parses a float env var, returning 0 when unset or unparsable.
func floatFromEnv(name string) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0
}

// int64FromEnv parses an int64 env var, returning 0 (guard off) when unset or
// unparsable.
func int64FromEnv(name string) int64 {
	if v := os.Getenv(name); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	}
	return 0
}
