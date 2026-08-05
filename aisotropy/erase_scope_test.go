package aisotropy

import (
	"context"
	"strings"
	"testing"
)

// TestEraseReportSingleReplicaIsComplete: with one process serving the
// deployment (the default), the working-memory erase really did cover every
// copy of the subject's buffer, so the receipt may say so.
func TestEraseReportSingleReplicaIsComplete(t *testing.T) {
	t.Setenv("AISOTROPY_REPLICAS", "")
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	seedSubject(t, rt, "acme", "alice")

	rep, err := app.EraseSubject(context.Background(), "acme", "alice")
	if err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	if rep.WorkingMemoryScope != EraseScopeProcessLocal {
		t.Errorf("WorkingMemoryScope = %q, want %q — working memory is embedded per process, always", rep.WorkingMemoryScope, EraseScopeProcessLocal)
	}
	if rep.DeclaredReplicas != 1 {
		t.Errorf("DeclaredReplicas = %d, want 1", rep.DeclaredReplicas)
	}
	if !rep.Complete {
		t.Errorf("Complete = false on a single-replica deployment; the erase genuinely covered everything")
	}
	if rep.Caveat != "" {
		t.Errorf("Caveat = %q, want empty when the erase is complete", rep.Caveat)
	}
}

// TestEraseReportMultiReplicaAdmitsPartialErasure is the fix: with N replicas
// each owning its own embedded working memory (BadgerDB takes an exclusive
// directory lock, so the store cannot be shared), App.EraseSubject reaches only
// the process that served the request. The receipt must SAY that instead of
// reporting a count that reads like "the subject is gone".
func TestEraseReportMultiReplicaAdmitsPartialErasure(t *testing.T) {
	t.Setenv("AISOTROPY_REPLICAS", "3")
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	seedSubject(t, rt, "acme", "alice")

	rep, err := app.EraseSubject(context.Background(), "acme", "alice")
	if err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	if rep.WorkingMemoryDeleted < 1 {
		t.Fatalf("nothing was erased locally: %+v", rep)
	}
	if rep.DeclaredReplicas != 3 {
		t.Errorf("DeclaredReplicas = %d, want 3", rep.DeclaredReplicas)
	}
	if rep.Complete {
		t.Fatal("Complete = true across a 3-replica fleet: the receipt claims a fleet-wide erasure that only touched this process")
	}
	if !strings.Contains(strings.ToLower(rep.Caveat), "replica") {
		t.Errorf("Caveat = %q, want it to name the un-erased replicas", rep.Caveat)
	}
}
