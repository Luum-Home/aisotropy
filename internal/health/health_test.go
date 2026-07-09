package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func okProbe(name string) Probe {
	return Probe{Name: name, Check: func(context.Context) error { return nil }}
}

func failProbe(name string, err error) Probe {
	return Probe{Name: name, Check: func(context.Context) error { return err }}
}

func TestRun_AllOK(t *testing.T) {
	c := New(time.Second, okProbe("facts"), okProbe("semantic"))
	rep := c.Run(context.Background())
	if rep.Status != StatusOK {
		t.Fatalf("status = %q, want ok", rep.Status)
	}
	if len(rep.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(rep.Checks))
	}
	// Probe order is preserved.
	if rep.Checks[0].Name != "facts" || rep.Checks[1].Name != "semantic" {
		t.Fatalf("check order not preserved: %+v", rep.Checks)
	}
}

func TestRun_OneDegradedMakesReportDegraded(t *testing.T) {
	c := New(time.Second, okProbe("facts"), failProbe("semantic", errors.New("dial tcp: refused")))
	rep := c.Run(context.Background())
	if rep.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded", rep.Status)
	}
	var sem Check
	for _, ck := range rep.Checks {
		if ck.Name == "semantic" {
			sem = ck
		}
	}
	if sem.Status != StatusDegraded || sem.Detail == "" {
		t.Fatalf("semantic check = %+v, want degraded with detail", sem)
	}
}

func TestRun_TimeoutIsDegraded(t *testing.T) {
	slow := Probe{Name: "slow", Check: func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	}}
	c := New(20*time.Millisecond, slow)
	rep := c.Run(context.Background())
	if rep.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded on timeout", rep.Status)
	}
}

func TestHandler_StatusCodes(t *testing.T) {
	tests := []struct {
		name     string
		checker  *Checker
		wantCode int
		wantBody Status
	}{
		{"ready", New(time.Second, okProbe("facts")), http.StatusOK, StatusOK},
		{"degraded", New(time.Second, failProbe("facts", errors.New("down"))), http.StatusServiceUnavailable, StatusDegraded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			tc.checker.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status code = %d, want %d", rec.Code, tc.wantCode)
			}
			var rep Report
			if err := json.NewDecoder(rec.Body).Decode(&rep); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if rep.Status != tc.wantBody {
				t.Fatalf("body status = %q, want %q", rep.Status, tc.wantBody)
			}
		})
	}
}
