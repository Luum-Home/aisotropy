package main

import (
	"strings"
	"testing"
)

// TestRunForgetful_OmittedScopeRefused is the OFFLINE (ARENA_STUB=1, zero
// network) executable self-audit for the isolation_forgetful dimension: it
// proves aisotropy lands in "refuses" BY CONSTRUCTION when the integrator omits
// the user scope on the recall.
//
// The refusal happens in api.ChatCore's very first action (chatIdentity →
// withIdentity → memory.Key.Validate → "memory: user requires a non-empty
// value"), BEFORE any model call — so it is fully observable against the stub,
// no provider needed. The error text must carry a signature the runner's
// matchForgetfulEnforcementSignature recognizes (runner/main.go), otherwise the
// runner would score this as an infrastructure failure instead of enforcement.
func TestRunForgetful_OmittedScopeRefused(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("AISOTROPY_DATA_DIR", "") // in-memory working memory for this test

	req := &request{
		Mode:      modeIsolationForgetful,
		SessionID: "arena-forget-selfaudit-b",
		UserID:    "", // the forgetful-integrator mistake: scope omitted
		Messages: []inMessage{
			{Role: "user", Content: "What is my favourite station?"},
		},
	}
	out := &result{Mode: modeIsolationForgetful}

	err := runForgetful(req, out)
	if err == nil {
		t.Fatal("runForgetful with a blank user_id returned nil — aisotropy must REFUSE the omitted scope at its own boundary (memory.Key.Validate), not answer it")
	}
	// The runner's enforcement signatures (main.go forgetfulEnforcementSignatures)
	// must recognize this text — assert the load-bearing substrings are present.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "requires a non-empty value") && !strings.Contains(msg, "invalid tenant/user identity") {
		t.Errorf("refusal error does not carry a recognized enforcement signature "+
			"(want 'requires a non-empty value' or 'invalid tenant/user identity'); got: %s", err.Error())
	}
}

// TestRunForgetful_ScopePresentSucceeds is the positive control: with a NON-blank
// user the same handler drives App.Turn to a normal reply (no refusal). This
// proves the refusal above is caused by the OMISSION, not by runForgetful always
// erroring — the symmetric-rigor property (a framework that does NOT enforce
// reaches the reply branch, where the runner scores leak vs silent_partition).
func TestRunForgetful_ScopePresentSucceeds(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("AISOTROPY_DATA_DIR", "") // in-memory working memory for this test

	req := &request{
		Mode:      modeIsolationForgetful,
		SessionID: "arena-forget-control-b",
		UserID:    "u-forget-control", // scope present → App.Turn must not refuse
		Messages: []inMessage{
			{Role: "user", Content: "What is my favourite station?"},
		},
	}
	out := &result{Mode: modeIsolationForgetful}

	if err := runForgetful(req, out); err != nil {
		t.Fatalf("runForgetful with a present user scope returned an error in the stub lane: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("runForgetful set out.Error with a present scope: %s", out.Error)
	}
	if strings.TrimSpace(out.Reply) == "" {
		t.Error("runForgetful with a present scope produced an empty reply — App.Turn should have answered")
	}
}
