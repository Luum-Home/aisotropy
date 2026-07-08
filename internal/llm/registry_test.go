package llm

import (
	"testing"
)

// clearProviderEnv unsets every env var RegistryFromEnv reads, so each test
// starts from a clean slate regardless of the environment the test binary
// happens to run in (e.g. a real ANTHROPIC_API_KEY exported in the shell).
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENROUTER_API_KEY", "OPENROUTER_MODEL",
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
		"AISOTROPY_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func TestRegistryFromEnv_NoCredentials_OnlyStub(t *testing.T) {
	clearProviderEnv(t)

	reg := RegistryFromEnv()

	names := reg.Names()
	if len(names) != 1 || names[0] != "stub" {
		t.Fatalf("Names() = %v, want [stub]", names)
	}
	if reg.DefaultName() != "stub" {
		t.Fatalf("DefaultName() = %q, want %q", reg.DefaultName(), "stub")
	}
	if reg.Default().Name() != "stub" {
		t.Fatalf("Default().Name() = %q, want %q", reg.Default().Name(), "stub")
	}
}

func TestRegistryFromEnv_AllCredentials_RegistersAllAndDefaultsToOpenRouter(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("OPENAI_API_KEY", "oa-key")
	t.Setenv("ANTHROPIC_API_KEY", "an-key")

	reg := RegistryFromEnv()

	names := reg.Names()
	if len(names) != 4 {
		t.Fatalf("Names() = %v, want 4 providers (openrouter, openai, anthropic, stub)", names)
	}
	for _, want := range []string{"openrouter", "openai", "anthropic", "stub"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("Get(%q) missing, want registered", want)
		}
	}
	if reg.DefaultName() != "openrouter" {
		t.Fatalf("DefaultName() = %q, want %q (highest precedence)", reg.DefaultName(), "openrouter")
	}
}

func TestRegistryFromEnv_OpenAIAndAnthropic_DefaultsToOpenAI(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oa-key")
	t.Setenv("ANTHROPIC_API_KEY", "an-key")

	reg := RegistryFromEnv()

	names := reg.Names()
	if len(names) != 3 {
		t.Fatalf("Names() = %v, want 3 providers (openai, anthropic, stub)", names)
	}
	if _, ok := reg.Get("openrouter"); ok {
		t.Fatalf("Get(%q) present, want absent (no OPENROUTER_API_KEY set)", "openrouter")
	}
	if reg.DefaultName() != "openai" {
		t.Fatalf("DefaultName() = %q, want %q", reg.DefaultName(), "openai")
	}
}

func TestRegistryFromEnv_OnlyAnthropic_DefaultsToAnthropic(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "an-key")

	reg := RegistryFromEnv()

	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("Names() = %v, want 2 providers (anthropic, stub)", names)
	}
	if reg.DefaultName() != "anthropic" {
		t.Fatalf("DefaultName() = %q, want %q", reg.DefaultName(), "anthropic")
	}
	c, ok := reg.Get("anthropic")
	if !ok {
		t.Fatal("Get(\"anthropic\") missing")
	}
	if c.Name() == "" {
		t.Error("anthropic client Name() is empty")
	}
}

func TestRegistry_RegisterGetNamesDefault(t *testing.T) {
	reg := NewRegistry()

	// Empty registry: no default set yet.
	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("Names() on empty registry = %v, want empty", got)
	}

	reg.Register("stub-a", NewStubClient())
	reg.Register("stub-b", NewStubClient())

	if reg.DefaultName() != "stub-a" {
		t.Fatalf("DefaultName() = %q, want %q (first registered)", reg.DefaultName(), "stub-a")
	}

	names := reg.Names()
	if len(names) != 2 || names[0] != "stub-a" || names[1] != "stub-b" {
		t.Fatalf("Names() = %v, want sorted [stub-a stub-b]", names)
	}

	if _, ok := reg.Get("missing"); ok {
		t.Fatal("Get(\"missing\") = true, want false")
	}

	if err := reg.SetDefault("stub-b"); err != nil {
		t.Fatalf("SetDefault(stub-b) error: %v", err)
	}
	if reg.DefaultName() != "stub-b" {
		t.Fatalf("DefaultName() after SetDefault = %q, want %q", reg.DefaultName(), "stub-b")
	}

	if err := reg.SetDefault("does-not-exist"); err == nil {
		t.Fatal("SetDefault(does-not-exist) error = nil, want error")
	}
}

func TestFromEnv_MatchesRegistryDefault(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "an-key")

	got := FromEnv()
	want := RegistryFromEnv().Default()

	if got.Name() != want.Name() {
		t.Fatalf("FromEnv().Name() = %q, want %q (RegistryFromEnv().Default())", got.Name(), want.Name())
	}
}
