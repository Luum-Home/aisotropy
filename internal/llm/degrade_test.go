package llm

import "testing"

const (
	testPrimary = "claude-sonnet-4-5"
	testCheap   = "claude-haiku-4-5-20251001"
)

func TestDegradeAtBelowThresholdUsesPrimary(t *testing.T) {
	got := DegradeAt(0.5, 0.95, testPrimary, testCheap)
	want := ModelChoice{Model: testPrimary, Degraded: false}
	if got != want {
		t.Fatalf("DegradeAt(0.5, 0.95, ...) = %+v, want %+v", got, want)
	}
}

func TestDegradeAtAtThresholdDegrades(t *testing.T) {
	got := DegradeAt(0.95, 0.95, testPrimary, testCheap)
	want := ModelChoice{Model: testCheap, Degraded: true}
	if got != want {
		t.Fatalf("DegradeAt(0.95, 0.95, ...) = %+v, want %+v", got, want)
	}
}

func TestDegradeAtAboveThresholdDegrades(t *testing.T) {
	got := DegradeAt(0.99, 0.95, testPrimary, testCheap)
	want := ModelChoice{Model: testCheap, Degraded: true}
	if got != want {
		t.Fatalf("DegradeAt(0.99, 0.95, ...) = %+v, want %+v", got, want)
	}
}

func TestDegradeAtEmptyCheapNeverDegrades(t *testing.T) {
	got := DegradeAt(1.0, 0.95, testPrimary, "")
	want := ModelChoice{Model: testPrimary, Degraded: false}
	if got != want {
		t.Fatalf("DegradeAt(1.0, 0.95, primary, \"\") = %+v, want %+v (empty cheap must never degrade)", got, want)
	}
}

func TestDegradeAtJustBelowThresholdUsesPrimary(t *testing.T) {
	got := DegradeAt(0.9499999, 0.95, testPrimary, testCheap)
	want := ModelChoice{Model: testPrimary, Degraded: false}
	if got != want {
		t.Fatalf("DegradeAt(0.9499999, 0.95, ...) = %+v, want %+v", got, want)
	}
}

func TestDegradeAtZeroPressureUsesPrimary(t *testing.T) {
	got := DegradeAt(0, 0.95, testPrimary, testCheap)
	want := ModelChoice{Model: testPrimary, Degraded: false}
	if got != want {
		t.Fatalf("DegradeAt(0, 0.95, ...) = %+v, want %+v", got, want)
	}
}

func TestDegradeAtZeroThresholdAlwaysDegrades(t *testing.T) {
	got := DegradeAt(0, 0, testPrimary, testCheap)
	want := ModelChoice{Model: testCheap, Degraded: true}
	if got != want {
		t.Fatalf("DegradeAt(0, 0, ...) = %+v, want %+v (threshold 0 means always degraded)", got, want)
	}
}

func TestDefaultDegradeThresholdIsPointNinetyFive(t *testing.T) {
	if DefaultDegradeThreshold != 0.95 {
		t.Fatalf("DefaultDegradeThreshold = %v, want 0.95", DefaultDegradeThreshold)
	}
}
