// Package health implements the readiness endpoint that distinguishes
// dependency health from mere liveness. cmd/aisotropyd already exposes
// GET /healthz as a liveness probe (the process is up); this package backs a
// separate GET /health readiness probe that actually exercises the networked
// dependencies (facts memory, semantic memory, ...) so a load balancer or
// orchestrator can tell "the process is running" apart from "the process can
// serve requests".
//
// Design constraints specific to aisotropy:
//   - Probes must be CHEAP and side-effect-free. In particular, no probe may
//     spend LLM tokens: actively calling the model on every readiness poll would
//     violate the "reject/degrade before spending tokens" constitutional gate
//     (.claude/rules/constitutional-gates.md). LLM health is therefore reported
//     as a static mode (real vs stub), never an active token-spending call.
//   - Probes run in parallel under a bounded timeout, so one slow/hung
//     dependency cannot make the readiness endpoint itself hang.
//   - A probe is a scoped, read-only operation against a reserved health
//     tenant, so it never reads or writes real tenant data.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status is the health verdict for one dependency or for the report as a whole.
type Status string

const (
	// StatusOK means the dependency responded successfully within the timeout.
	StatusOK Status = "ok"
	// StatusDegraded means the dependency errored or timed out.
	StatusDegraded Status = "degraded"
)

// Probe is a single named dependency check. Check must be read-only and cheap;
// it returns nil when the dependency is healthy, or an error describing the
// failure. Check MUST honor ctx cancellation (the Checker bounds every probe
// with a timeout).
type Probe struct {
	Name  string
	Check func(ctx context.Context) error
}

// Check is the per-dependency result reported in the readiness response.
type Check struct {
	Name      string `json:"name"`
	Status    Status `json:"status"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// Report is the full readiness response body.
type Report struct {
	Status Status  `json:"status"`
	Checks []Check `json:"checks"`
}

// Checker runs a fixed set of probes, each bounded by timeout, and reports an
// aggregate readiness verdict. It is safe for concurrent use.
type Checker struct {
	probes  []Probe
	timeout time.Duration
}

// New returns a Checker that runs each probe with the given per-probe timeout.
// A non-positive timeout defaults to 2s.
func New(timeout time.Duration, probes ...Probe) *Checker {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Checker{probes: probes, timeout: timeout}
}

// Run executes every probe in parallel (each under its own timeout derived from
// ctx) and returns the aggregate Report. The report Status is StatusOK only if
// every probe succeeded; any failure makes it StatusDegraded. Checks are ordered
// to match the probe order, so the response is stable across calls.
func (c *Checker) Run(ctx context.Context) Report {
	checks := make([]Check, len(c.probes))
	var wg sync.WaitGroup
	for i, p := range c.probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			checks[i] = c.runProbe(ctx, p)
		}(i, p)
	}
	wg.Wait()

	report := Report{Status: StatusOK, Checks: checks}
	for _, ck := range checks {
		if ck.Status != StatusOK {
			report.Status = StatusDegraded
			break
		}
	}
	return report
}

func (c *Checker) runProbe(ctx context.Context, p Probe) Check {
	probeCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	err := p.Check(probeCtx)
	latency := time.Since(start).Milliseconds()

	ck := Check{Name: p.Name, Status: StatusOK, LatencyMS: latency}
	if err != nil {
		ck.Status = StatusDegraded
		ck.Detail = err.Error()
	}
	return ck
}

// Handler returns an http.Handler serving the readiness report as JSON. It
// responds 200 when the aggregate status is OK and 503 (Service Unavailable)
// when any dependency is degraded, so a load balancer can gate traffic on the
// status code alone without parsing the body.
func (c *Checker) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := c.Run(r.Context())

		w.Header().Set("Content-Type", "application/json")
		if report.Status == StatusOK {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(report)
	}
}
