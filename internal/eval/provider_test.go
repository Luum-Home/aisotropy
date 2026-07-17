package eval

import (
	"strings"
	"testing"
)

// clearProviderEnv unsets every provider credential and the judge-model
// override for a test, so JudgeClientFromEnv resolves deterministically
// regardless of the developer's shell.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENROUTER_API_KEY", "OPENROUTER_MODEL",
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
		"AISOTROPY_MODEL", JudgeModelEnv,
	} {
		t.Setenv(k, "")
	}
}

// TestJudgeClientFromEnv_NoKeyIsStub is the skip-cleanly contract: with no
// provider key configured, the judge resolves to the keyless stub and Real is
// false — the signal a harness uses to label a run "not real quality".
func TestJudgeClientFromEnv_NoKeyIsStub(t *testing.T) {
	clearProviderEnv(t)

	jc := JudgeClientFromEnv()
	if jc.Real {
		t.Fatalf("Real = true with no provider key; want false")
	}
	if jc.Provider != providerStub {
		t.Fatalf("Provider = %q; want %q", jc.Provider, providerStub)
	}
	if jc.Client == nil {
		t.Fatal("Client is nil; the stub must always be returned")
	}
}

// TestJudgeClientFromEnv_ProviderAgnosticPrecedence proves the gate is NOT
// hardcoded to Anthropic: any single provider key flips Real to true and selects
// that provider, and the documented precedence (openrouter > openai > anthropic)
// holds when several are set at once.
func TestJudgeClientFromEnv_ProviderAgnosticPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		provider string
	}{
		{"anthropic-only", map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, providerAnthropic},
		{"openai-only", map[string]string{"OPENAI_API_KEY": "sk-test"}, providerOpenAI},
		{"openrouter-only", map[string]string{"OPENROUTER_API_KEY": "sk-test"}, providerOpenRouter},
		{"precedence-openrouter-wins", map[string]string{
			"OPENROUTER_API_KEY": "sk-a", "OPENAI_API_KEY": "sk-b", "ANTHROPIC_API_KEY": "sk-c",
		}, providerOpenRouter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			jc := JudgeClientFromEnv()
			if !jc.Real {
				t.Fatalf("Real = false with %v set; want true", tc.env)
			}
			if jc.Provider != tc.provider {
				t.Fatalf("Provider = %q; want %q", jc.Provider, tc.provider)
			}
			if jc.Client == nil {
				t.Fatal("Client is nil")
			}
		})
	}
}

// TestJudgeClientFromEnv_ModelOverride proves AISOTROPY_JUDGE_MODEL swaps only
// the model id on the selected real provider — the "serve cheap, judge strong"
// path — surfacing the override model in the client Name.
func TestJudgeClientFromEnv_ModelOverride(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv(JudgeModelEnv, "claude-opus-4-test")

	jc := JudgeClientFromEnv()
	if !jc.Real || jc.Provider != providerAnthropic {
		t.Fatalf("provider = %q real = %v; want anthropic/real", jc.Provider, jc.Real)
	}
	if name := jc.Client.Name(); !strings.Contains(name, "claude-opus-4-test") {
		t.Fatalf("client Name = %q; want it to carry the override model id", name)
	}
}

// TestJudgeClientFromEnv_OverrideIgnoredOnStub proves a judge-model override
// with NO provider key does not fabricate a real client — it stays on the stub.
func TestJudgeClientFromEnv_OverrideIgnoredOnStub(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(JudgeModelEnv, "some-model")

	jc := JudgeClientFromEnv()
	if jc.Real || jc.Provider != providerStub {
		t.Fatalf("override without a key produced provider = %q real = %v; want stub/false", jc.Provider, jc.Real)
	}
}
