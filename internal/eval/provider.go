package eval

import (
	"os"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// JudgeModelEnv is the env var an operator sets to run the LLM-as-a-judge on a
// DIFFERENT model than the one under evaluation (a common and recommended
// setup: judging with a stronger/independent model reduces the self-preference
// bias a model has when grading its own output). Empty ⇒ the judge runs on the
// same provider default the product serves with.
const JudgeModelEnv = "AISOTROPY_JUDGE_MODEL"

// Registry provider names, mirroring the (unexported) keys llm.RegistryFromEnv
// registers under and llm.Registry.DefaultName returns. Kept as local constants
// because the llm package does not export them; they are a stable contract of
// that package (asserted by its own registry_test.go).
const (
	providerOpenRouter = "openrouter"
	providerOpenAI     = "openai"
	providerAnthropic  = "anthropic"
	providerStub       = "stub"
)

// JudgeClient bundles the client an LLM-as-a-judge runs on with the two honesty
// signals a report needs: the resolved provider/model Name and whether it is
// the credential-less stub (Real == false). A harness uses Real to loudly label
// a run as "stub — proves wiring, NOT real quality" when no provider key is
// configured, so a green report can never be mistaken for a measured one.
type JudgeClient struct {
	// Client is the provider-agnostic llm.Client the judge drives.
	Client llm.Client
	// Provider is the registry provider name that was selected:
	// "openrouter" | "openai" | "anthropic" | "stub".
	Provider string
	// Real is true when a real provider credential is configured — i.e. the
	// selected provider is NOT the keyless stub. False means every judge score
	// is a deterministic stub artifact, not a measured quality signal.
	Real bool
}

// JudgeClientFromEnv selects the LLM-as-a-judge client the SAME
// provider-agnostic way the product selects its serving client — via
// llm.RegistryFromEnv's precedence (OPENROUTER_API_KEY > OPENAI_API_KEY >
// ANTHROPIC_API_KEY > stub, ADR-0009) — NOT a hardcoded ANTHROPIC_API_KEY gate.
// This is the shared seam that keeps every eval judge (bench/answer-quality and
// bench/rag-quality) honest and portable: whatever provider the operator has a
// key for is the one that judges, and with zero keys the harness falls back to
// the stub and can label itself accordingly.
//
// When AISOTROPY_JUDGE_MODEL is set AND a real provider is available, the judge
// is built on that same provider swapping ONLY the model id — so an operator can
// serve with a cheap model but judge with a stronger one, on one key. With no
// override (or on the stub) the registry default is returned unchanged.
func JudgeClientFromEnv() JudgeClient {
	reg := llm.RegistryFromEnv()
	provider := reg.DefaultName()
	real := provider != providerStub

	override := os.Getenv(JudgeModelEnv)
	if !real || override == "" {
		return JudgeClient{Client: reg.Default(), Provider: provider, Real: real}
	}

	if c := judgeClientWithModel(provider, override); c != nil {
		return JudgeClient{Client: c, Provider: provider, Real: real}
	}
	// Override requested but the provider could not be rebuilt (missing key for
	// the resolved provider — should not happen given real==true, but stay
	// fail-safe): fall back to the registry default rather than returning nil.
	return JudgeClient{Client: reg.Default(), Provider: provider, Real: real}
}

// judgeClientWithModel builds a fresh client on the named provider using model,
// reusing the same env credentials RegistryFromEnv detected. Returns nil if the
// provider is unknown or its key is unset (the caller falls back).
func judgeClientWithModel(provider, model string) llm.Client {
	switch provider {
	case providerOpenRouter:
		if k := os.Getenv("OPENROUTER_API_KEY"); k != "" {
			return llm.NewOpenRouterClient(k, model)
		}
	case providerOpenAI:
		if k := os.Getenv("OPENAI_API_KEY"); k != "" {
			return llm.NewOpenAICompatClient(k, model, os.Getenv("OPENAI_BASE_URL"), providerOpenAI)
		}
	case providerAnthropic:
		if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
			return llm.NewAnthropicClient(k, model)
		}
	}
	return nil
}
