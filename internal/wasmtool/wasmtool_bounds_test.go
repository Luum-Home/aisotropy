package wasmtool_test

// Resource-bound (DoS-defense) coverage for wasmtool: the HARD memory ceiling
// (WithMemoryLimitPages) and the per-call DEADLINE that bounds CPU (wazero has
// no instruction metering, so the deadline is the only CPU bound). Both fixtures
// are compiled at test time via buildGuest, so they skip under -short.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

// TestTool_MemoryLimitBoundsAllocation proves the HARD memory bound: a guest that
// keeps growing its linear memory past WithMemoryLimitPages traps, and the host
// surfaces a dispatch error instead of allowing an unbounded host allocation.
func TestTool_MemoryLimitBoundsAllocation(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:             "hog",
		Module:           buildGuest(t, "hogmod"),
		Capabilities:     wasmtool.Capabilities{AllowWASI: true},
		MemoryLimitPages: wasmtool.DefaultMemoryLimitPages, // 256 MiB cap
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("guest grew memory past the page limit without error — the memory bound did NOT hold")
	}
}

// TestTool_CallTimeoutBoundsSpin proves the CPU bound is the DEADLINE: a guest
// that spins forever is torn down at CallTimeout (wazero has no CPU metering), so
// dispatch returns an error at ~the deadline instead of hanging.
func TestTool_CallTimeoutBoundsSpin(t *testing.T) {
	ctx := context.Background()
	const timeout = 300 * time.Millisecond
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "spin",
		Module:       buildGuest(t, "spinmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
		CallTimeout:  timeout,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	start := time.Now()
	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("spinning guest returned no error — the deadline did NOT bound it")
	}
	// It was the run call that was torn down (dispatch wraps run errors with the
	// entry-point name), not an earlier failure.
	if !strings.Contains(err.Error(), "aisotropy_run") {
		t.Fatalf("expected the error to name the run entry point (deadline tore down the spin), got: %v", err)
	}
	// Bounded at ~the deadline, NOT hung. The generous ceiling tolerates slow CI
	// while still proving the guest did not run unbounded.
	if elapsed > 5*time.Second {
		t.Fatalf("spin ran %v — the deadline did not bound it", elapsed)
	}
	// And it did not fail instantly: the guest actually reached the spin and was
	// stopped BY the deadline, not by some earlier error.
	if elapsed < timeout/2 {
		t.Fatalf("dispatch returned in %v, well before the %v deadline — not the deadline path", elapsed, timeout)
	}
}
