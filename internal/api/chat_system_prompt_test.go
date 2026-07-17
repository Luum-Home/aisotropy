package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// TestChatBuildSystemPrompt_AppendsScopeGatedTools covers the tool-gating branch
// of chatBuildSystemPrompt: a permitted tool's fragment is appended to the
// hydrated system prompt, and a tool the caller's scopes do not cover is never
// mentioned (granular control). Driven directly with nil stores so only the
// persona base + the tool fragment contribute.
func TestChatBuildSystemPrompt_AppendsScopeGatedTools(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	reg := tools.NewRegistry(
		tools.Tool{Name: "search_docs", Description: "search the docs"},                          // always permitted (empty scope)
		tools.Tool{Name: "delete_account", Description: "danger", RequiredScope: "admin:delete"}, // gated
	)

	t.Run("permitted tool appears, gated tool hidden", func(t *testing.T) {
		in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{ProductID: "p1"}}
		sys, err := chatBuildSystemPrompt(context.Background(), nil, nil, reg, nil, key, scope, in, "hello")
		if err != nil {
			t.Fatalf("chatBuildSystemPrompt: %v", err)
		}
		if !strings.Contains(sys, "search_docs") {
			t.Errorf("system prompt missing the permitted tool fragment: %q", sys)
		}
		if strings.Contains(sys, "delete_account") {
			t.Errorf("gated tool leaked into the prompt without its scope: %q", sys)
		}
	})

	t.Run("granting the scope surfaces the gated tool", func(t *testing.T) {
		in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{ProductID: "p1", Scopes: []string{"admin:delete"}}}
		sys, err := chatBuildSystemPrompt(context.Background(), nil, nil, reg, nil, key, scope, in, "hello")
		if err != nil {
			t.Fatalf("chatBuildSystemPrompt: %v", err)
		}
		if !strings.Contains(sys, "delete_account") {
			t.Errorf("granted scope did not surface the gated tool: %q", sys)
		}
	})

	t.Run("nil registry appends no tool fragment", func(t *testing.T) {
		in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{ProductID: "p1"}}
		sys, err := chatBuildSystemPrompt(context.Background(), nil, nil, nil, nil, key, scope, in, "hello")
		if err != nil {
			t.Fatalf("chatBuildSystemPrompt: %v", err)
		}
		if strings.Contains(sys, "search_docs") {
			t.Errorf("nil registry must add no tool fragment: %q", sys)
		}
	})
}
