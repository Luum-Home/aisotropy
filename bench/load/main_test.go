package main

// main_test.go covers the bench/load harness:
//
//   - Pure helpers (pct, flatten, dms, chatBody, scenarioResult accessors,
//     tenantName, parseFlags) get direct table-driven tests.
//   - reporter's formatting methods are tested by pointing rep.w at a
//     bytes.Buffer — no real I/O needed.
//   - The concurrency + aggregation logic (driveLoad, driveQuotaGate,
//     driveIsolationCheck) and the individual do*/seed*/stream* request
//     helpers are tested against a fake httpDoer (funcDoer below), so the
//     hot-path logic is exercised without a real server.
//   - freeAddr/waitForServer/bootApp/runEmbedded/runEmbeddedOnDisk/
//     runQuotaGate/runIsolationUnderLoad and run() itself are covered by a
//     small number of real, in-process aisotropy.App boots (the same pattern
//     aisotropy/isolation_e2e_test.go uses) with tiny durations/concurrency
//     so the suite stays fast.
import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// ---------------------------------------------------------------------------
// fake httpDoer
// ---------------------------------------------------------------------------

// funcDoer adapts a plain function to httpDoer so each test can script exactly
// the responses/errors it needs without a real network round trip.
type funcDoer func(req *http.Request) (*http.Response, error)

func (f funcDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func newResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestPct(t *testing.T) {
	if got := pct(nil, 50); got != 0 {
		t.Fatalf("pct(nil, 50) = %v, want 0", got)
	}
	d := []time.Duration{
		5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond,
		4 * time.Millisecond, 2 * time.Millisecond,
	}
	if got := pct(d, 0); got != 1*time.Millisecond {
		t.Fatalf("pct p0 = %v, want 1ms", got)
	}
	if got := pct(d, 100); got != 5*time.Millisecond {
		t.Fatalf("pct p100 (max) = %v, want 5ms", got)
	}
	if got := pct(d, 200); got != 5*time.Millisecond {
		t.Fatalf("pct p200 (clamped to max) = %v, want 5ms", got)
	}
	// Original slice must be untouched (pct copies before sorting).
	if d[0] != 5*time.Millisecond {
		t.Fatalf("pct mutated its input: d[0] = %v, want 5ms", d[0])
	}
	single := []time.Duration{7 * time.Millisecond}
	if got := pct(single, 99); got != 7*time.Millisecond {
		t.Fatalf("pct single-element p99 = %v, want 7ms", got)
	}
}

func TestFlatten(t *testing.T) {
	if got := flatten(nil); len(got) != 0 {
		t.Fatalf("flatten(nil) len = %d, want 0", len(got))
	}
	in := [][]time.Duration{
		{1 * time.Millisecond, 2 * time.Millisecond},
		nil,
		{3 * time.Millisecond},
	}
	got := flatten(in)
	want := []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	if len(got) != len(want) {
		t.Fatalf("flatten len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("flatten[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestDms(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"},
		{500 * time.Microsecond, "500us"},
		{1500 * time.Microsecond, "1.50ms"},
		{25 * time.Millisecond, "25.00ms"},
	}
	for _, c := range cases {
		if got := dms(c.d); got != c.want {
			t.Errorf("dms(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestChatBody(t *testing.T) {
	got := string(chatBody("hello there"))
	if !strings.Contains(got, `"prompt":"hello there"`) {
		t.Fatalf("chatBody missing prompt: %s", got)
	}
	if !strings.Contains(got, `"product_id":"load-test"`) || !strings.Contains(got, `"session_id":"load"`) {
		t.Fatalf("chatBody missing stable session keys: %s", got)
	}
}

func TestScenarioResultAccessors(t *testing.T) {
	zero := scenarioResult{}
	if got := zero.throughput(); got != 0 {
		t.Fatalf("zero.throughput() = %v, want 0", got)
	}
	if got := zero.errRate(); got != 0 {
		t.Fatalf("zero.errRate() = %v, want 0", got)
	}
	r := scenarioResult{total: 200, errs: 50, elapsed: 2 * time.Second}
	if got := r.throughput(); got != 100 {
		t.Fatalf("throughput = %v, want 100", got)
	}
	if got := r.errRate(); got != 25 {
		t.Fatalf("errRate = %v, want 25", got)
	}
}

func TestTenantName(t *testing.T) {
	if got := tenantName(3); got != "isotenant3" {
		t.Fatalf("tenantName(3) = %q, want isotenant3", got)
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-dur=250ms", "-quick", "-users=3", "-out=custom.txt", "-maxconc=7"})
	if err != nil {
		t.Fatalf("parseFlags returned error: %v", err)
	}
	if cfg.dur != 250*time.Millisecond {
		t.Errorf("dur = %v, want 250ms", cfg.dur)
	}
	if !cfg.quick {
		t.Errorf("quick = false, want true")
	}
	if cfg.users != 3 {
		t.Errorf("users = %d, want 3", cfg.users)
	}
	if cfg.out != "custom.txt" {
		t.Errorf("out = %q, want custom.txt", cfg.out)
	}
	if cfg.maxConc != 7 {
		t.Errorf("maxConc = %d, want 7", cfg.maxConc)
	}

	defCfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil) returned error: %v", err)
	}
	if defCfg.dur != 4*time.Second || defCfg.users != 8 || defCfg.out != "results.txt" || defCfg.maxConc != 1024 || defCfg.quick {
		t.Fatalf("unexpected defaults: %+v", defCfg)
	}

	if _, err := parseFlags([]string{"-not-a-real-flag"}); err == nil {
		t.Fatal("parseFlags with an unknown flag: expected error, got nil")
	}
}

func TestResolveSweepConfig(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		cfg := &config{dur: 3 * time.Second, maxConc: 900}
		dur, tenantSweep, concRamp := resolveSweepConfig(cfg)
		if dur != 3*time.Second {
			t.Errorf("dur = %v, want 3s", dur)
		}
		wantTenants := []int{10, 100, 1000}
		if fmt.Sprint(tenantSweep) != fmt.Sprint(wantTenants) {
			t.Errorf("tenantSweep = %v, want %v", tenantSweep, wantTenants)
		}
		wantConc := []int{16, 64, 256, 512, 900}
		if fmt.Sprint(concRamp) != fmt.Sprint(wantConc) {
			t.Errorf("concRamp = %v, want %v", concRamp, wantConc)
		}
	})
	t.Run("quick overrides dur and shrinks the sweep", func(t *testing.T) {
		cfg := &config{dur: 3 * time.Second, quick: true, maxConc: 900}
		dur, tenantSweep, concRamp := resolveSweepConfig(cfg)
		if dur != 2*time.Second {
			t.Errorf("quick dur = %v, want 2s (forced)", dur)
		}
		wantTenants := []int{10, 100}
		if fmt.Sprint(tenantSweep) != fmt.Sprint(wantTenants) {
			t.Errorf("quick tenantSweep = %v, want %v", tenantSweep, wantTenants)
		}
		wantConc := []int{16, 64, 256}
		if fmt.Sprint(concRamp) != fmt.Sprint(wantConc) {
			t.Errorf("quick concRamp = %v, want %v", concRamp, wantConc)
		}
	})
}

// ---------------------------------------------------------------------------
// reporter
// ---------------------------------------------------------------------------

func TestReporterHeaderSectionTableFooter(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	rep.header(3 * time.Second)
	rep.section("A", "Title", "line one", "line two")
	rep.table([]scenarioResult{
		{label: "row1", total: 10, errs: 1, elapsed: time.Second, p50: time.Millisecond, p95: 2 * time.Millisecond, p99: 3 * time.Millisecond, rejected429: 2},
	})
	rep.verdict(true, "it passed")
	rep.verdict(false, "it failed")
	rep.footer()

	out := buf.String()
	for _, want := range []string{
		"aisotropy FULL-RUNTIME multi-tenant LOAD TEST",
		"SCENARIO A: Title",
		"line one",
		"line two",
		"row1",
		"VERDICT [PASS]: it passed",
		"VERDICT [FAIL]: it failed",
		"Finished:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestReporterBreakingPoint_Empty(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	rep.breakingPoint(nil)
	if buf.Len() != 0 {
		t.Fatalf("breakingPoint(nil) wrote output, want none: %s", buf.String())
	}
}

func TestReporterBreakingPoint_NotReached(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	rows := []scenarioResult{
		{label: "conc=16", total: 100, errs: 0, p99: 5 * time.Millisecond},
		{label: "conc=64", total: 100, errs: 0, p99: 6 * time.Millisecond},
	}
	rep.breakingPoint(rows)
	out := buf.String()
	if !strings.Contains(out, "BREAKING POINT: not reached up to conc=64") {
		t.Fatalf("expected 'not reached' verdict, got: %s", out)
	}
	if !strings.Contains(out, "highest-load point conc=64") {
		t.Fatalf("expected highest-load point summary, got: %s", out)
	}
}

func TestReporterBreakingPoint_Reached(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	rows := []scenarioResult{
		{label: "conc=16", total: 100, errs: 0, p99: 5 * time.Millisecond},
		{label: "conc=512", total: 100, errs: 60, p99: 500 * time.Millisecond},
	}
	rep.breakingPoint(rows)
	out := buf.String()
	if !strings.Contains(out, "BREAKING POINT: conc=512") {
		t.Fatalf("expected breaking-point verdict at conc=512, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// do*/seed*/stream* helpers against a fake httpDoer
// ---------------------------------------------------------------------------

func TestDoChat(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", req.Method)
			}
			return newResp(http.StatusOK, "ok"), nil
		})
		d, status, err := doChat(doer, "http://fake", "t1", "u1", "s1")
		if err != nil {
			t.Fatalf("doChat error: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if d < 0 {
			t.Fatalf("negative duration: %v", d)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		})
		_, _, err := doChat(doer, "http://fake", "t1", "u1", "s1")
		if err == nil {
			t.Fatal("expected transport error, got nil")
		}
	})
	t.Run("non-200 status", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return newResp(http.StatusInternalServerError, ""), nil
		})
		_, status, err := doChat(doer, "http://fake", "t1", "u1", "s1")
		if err != nil {
			t.Fatalf("doChat error: %v", err)
		}
		if status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", status)
		}
	})
}

func TestDoStream(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("Accept header = %q, want text/event-stream", req.Header.Get("Accept"))
			}
			return newResp(http.StatusOK, "data: chunk\n\n"), nil
		})
		total, ttft, status, err := doStream(doer, "http://fake", "t1", "u1", "s1")
		if err != nil {
			t.Fatalf("doStream error: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if ttft > total {
			t.Fatalf("ttft (%v) > total (%v)", ttft, total)
		}
	})
	t.Run("non-200 status skips TTFT read", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return newResp(http.StatusTooManyRequests, ""), nil
		})
		_, ttft, status, err := doStream(doer, "http://fake", "t1", "u1", "s1")
		if err != nil {
			t.Fatalf("doStream error: %v", err)
		}
		if status != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", status)
		}
		if ttft != 0 {
			t.Fatalf("ttft = %v, want 0 for non-200 response", ttft)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		})
		_, _, status, err := doStream(doer, "http://fake", "t1", "u1", "s1")
		if err == nil {
			t.Fatal("expected transport error, got nil")
		}
		if status != 0 {
			t.Fatalf("status = %d, want 0 on transport error", status)
		}
	})
}

func TestSeedMarker(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(b), "verbatim: secret1") {
				t.Errorf("seed body missing marker: %s", b)
			}
			return newResp(http.StatusOK, ""), nil
		})
		if err := seedMarker(doer, "http://fake", "tenantA", "s1", "secret1"); err != nil {
			t.Fatalf("seedMarker error: %v", err)
		}
	})
	t.Run("non-200 is an error", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return newResp(http.StatusInternalServerError, ""), nil
		})
		if err := seedMarker(doer, "http://fake", "tenantA", "s1", "secret1"); err == nil {
			t.Fatal("expected error for non-200 seed response, got nil")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		})
		if err := seedMarker(doer, "http://fake", "tenantA", "s1", "secret1"); err == nil {
			t.Fatal("expected transport error, got nil")
		}
	})
}

func TestStreamBody(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return newResp(http.StatusOK, "hello-marker"), nil
		})
		body, status, err := streamBody(doer, "http://fake", "tenantA", "s1", "prompt")
		if err != nil {
			t.Fatalf("streamBody error: %v", err)
		}
		if status != http.StatusOK || body != "hello-marker" {
			t.Fatalf("status=%d body=%q, want 200/hello-marker", status, body)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		doer := funcDoer(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		})
		if _, _, err := streamBody(doer, "http://fake", "tenantA", "s1", "prompt"); err == nil {
			t.Fatal("expected transport error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// driveLoad / driveQuotaGate / driveIsolationCheck against a fake httpDoer
// ---------------------------------------------------------------------------

// cyclingDoer round-robins through error / 429 / 500 / 200 responses so a
// short run exercises every branch of driveLoad's result-classification switch.
func cyclingDoer(counter *int64) funcDoer {
	return func(req *http.Request) (*http.Response, error) {
		i := atomic.AddInt64(counter, 1)
		switch i % 4 {
		case 0:
			return nil, errors.New("simulated network error")
		case 1:
			return newResp(http.StatusTooManyRequests, ""), nil
		case 2:
			return newResp(http.StatusInternalServerError, "boom"), nil
		default:
			return newResp(http.StatusOK, "chunk-of-data-for-ttft-read"), nil
		}
	}
}

func TestDriveLoad_Fake(t *testing.T) {
	for _, mix := range []reqMix{mix5050, mixChatOnly, mixStreamOnly} {
		mix := mix
		t.Run(fmt.Sprintf("mix=%d", mix), func(t *testing.T) {
			var buf bytes.Buffer
			rep := &reporter{w: &buf}
			var n int64
			cfg := embedCfg{tenants: 5, users: 2, conc: 4, dur: 150 * time.Millisecond, mix: mix, label: "fake"}
			res := driveLoad(rep, "http://fake", cyclingDoer(&n), cfg)

			if res.total == 0 {
				t.Fatal("expected total > 0")
			}
			if res.errs == 0 {
				t.Fatal("expected errs > 0 (network errors + 500s are both classified as errors)")
			}
			if res.rejected429 == 0 {
				t.Fatal("expected rejected429 > 0")
			}
			if res.total < res.errs+res.rejected429 {
				t.Fatalf("total (%d) < errs+rejected429 (%d)", res.total, res.errs+res.rejected429)
			}
			if res.peakGoroutines < res.baseGoroutines {
				t.Fatalf("peakGoroutines (%d) < baseGoroutines (%d)", res.peakGoroutines, res.baseGoroutines)
			}
		})
	}
}

func TestDriveQuotaGate_Fake(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	var n int64
	doer := funcDoer(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/tenant/calm/") {
			return newResp(http.StatusOK, ""), nil
		}
		i := atomic.AddInt64(&n, 1)
		switch i % 4 {
		case 0:
			return nil, errors.New("simulated network error")
		case 1:
			return newResp(http.StatusInternalServerError, ""), nil
		case 2:
			return newResp(http.StatusOK, ""), nil
		default:
			return newResp(http.StatusTooManyRequests, ""), nil
		}
	})
	driveQuotaGate(rep, doer, "http://fake", 100*time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, "hammered tenant:") {
		t.Fatalf("missing hammered-tenant summary: %s", out)
	}
	if !strings.Contains(out, "control tenant 'calm' (single request): HTTP 200") {
		t.Fatalf("missing/incorrect control-tenant line: %s", out)
	}
	if !strings.Contains(out, "VERDICT") {
		t.Fatalf("missing VERDICT line: %s", out)
	}
}

var (
	isoTenantRe = regexp.MustCompile(`/tenant/([^/]+)/`)
	isoMarkerRe = regexp.MustCompile(`verbatim: (\S+)`)
)

// newIsolationFakeDoer builds a doer that records the marker seeded per tenant
// (from the /chat seed request bodies) and then, for the streamed read calls,
// round-robins through: a transport error, a non-200 status, a clean positive
// control, a positive control WITH a deliberately injected leak of another
// tenant's marker, and a miss (no marker at all) — exercising every branch of
// driveIsolationCheck's classification logic.
func newIsolationFakeDoer() httpDoer {
	var mu sync.Mutex
	markers := map[string]string{}
	var calls int64
	return funcDoer(func(req *http.Request) (*http.Response, error) {
		m := isoTenantRe.FindStringSubmatch(req.URL.Path)
		if m == nil {
			return newResp(http.StatusBadRequest, ""), nil
		}
		tenant := m[1]
		if strings.HasSuffix(req.URL.Path, "/chat") {
			b, _ := io.ReadAll(req.Body)
			if mm := isoMarkerRe.FindStringSubmatch(string(b)); len(mm) > 1 {
				mu.Lock()
				markers[tenant] = mm[1]
				mu.Unlock()
			}
			return newResp(http.StatusOK, ""), nil
		}
		n := atomic.AddInt64(&calls, 1)
		switch n % 5 {
		case 0:
			return nil, errors.New("simulated network error")
		case 1:
			return newResp(http.StatusInternalServerError, ""), nil
		case 2:
			mu.Lock()
			own := markers[tenant]
			mu.Unlock()
			return newResp(http.StatusOK, own), nil
		case 3:
			mu.Lock()
			own := markers[tenant]
			var other string
			for tt, mk := range markers {
				if tt != tenant {
					other = mk
					break
				}
			}
			mu.Unlock()
			return newResp(http.StatusOK, own+" "+other), nil
		default:
			return newResp(http.StatusOK, "nothing interesting here"), nil
		}
	})
}

func TestDriveIsolationCheck_Fake(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	driveIsolationCheck(rep, newIsolationFakeDoer(), "http://fake", 200*time.Millisecond)

	out := buf.String()
	for _, want := range []string{
		"interleaved streamed reads across 10 tenants",
		"positive control:",
		"cross-tenant leaks detected:",
		"VERDICT",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("driveIsolationCheck output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDriveIsolationCheck_SeedFails(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	doer := funcDoer(func(req *http.Request) (*http.Response, error) {
		return newResp(http.StatusInternalServerError, ""), nil
	})
	driveIsolationCheck(rep, doer, "http://fake", 50*time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, "seed FAILED for isotenant0") {
		t.Fatalf("expected seed FAILED line, got: %s", out)
	}
	if strings.Contains(out, "VERDICT") {
		t.Fatalf("did not expect a VERDICT line after an early seed failure: %s", out)
	}
}

// ---------------------------------------------------------------------------
// freeAddr / waitForServer / bootApp (real network, no aisotropy.App needed
// for the failure/timeout branches)
// ---------------------------------------------------------------------------

func TestFreeAddr(t *testing.T) {
	addr, err := freeAddr()
	if err != nil {
		t.Fatalf("freeAddr error: %v", err)
	}
	if !strings.Contains(addr, ":") {
		t.Fatalf("freeAddr() = %q, want host:port", addr)
	}
}

func TestWaitForServer_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitForServer(srv.Client(), srv.URL+"/healthz", time.Second); err != nil {
		t.Fatalf("waitForServer error: %v", err)
	}
}

func TestWaitForServer_Timeout(t *testing.T) {
	addr, err := freeAddr()
	if err != nil {
		t.Fatalf("freeAddr error: %v", err)
	}
	// Nothing is listening on addr, so every attempt fails until the timeout.
	err = waitForServer(&http.Client{Timeout: 50 * time.Millisecond}, "http://"+addr+"/healthz", 120*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "not ready within") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootApp_Failure(t *testing.T) {
	// An invalid listen address makes the App's ListenAndServe fail immediately;
	// bootApp still health-checks the ORIGINAL freeAddr()-derived address (nothing
	// ever listens there), so a short healthTimeout resolves the failure branch
	// quickly instead of waiting out the production 10s default.
	base, client, shutdown, err := bootApp(2, 200*time.Millisecond, aisotropy.WithAddr("this-is-not-a-valid-tcp-address"))
	if err == nil {
		if shutdown != nil {
			shutdown()
		}
		t.Fatal("expected bootApp to fail for an invalid listen address, got nil error")
	}
	if base != "" || client != nil || shutdown != nil {
		t.Fatalf("expected zero values on failure, got base=%q client=%v shutdown!=nil=%v", base, client, shutdown != nil)
	}
}

func TestBootApp_Success(t *testing.T) {
	base, client, shutdown, err := bootApp(2, defaultHealthTimeout)
	if err != nil {
		t.Fatalf("bootApp error: %v", err)
	}
	defer shutdown()
	if base == "" || client == nil {
		t.Fatalf("expected non-empty base and client, got base=%q client=%v", base, client)
	}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz GET error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// runEmbedded / runEmbeddedOnDisk / runQuotaGate / runIsolationUnderLoad
// (real, tiny aisotropy.App instances — same boot pattern used throughout the
// aisotropy/*_e2e_test.go suite, just with minimal duration/concurrency so the
// unit-test suite stays fast).
// ---------------------------------------------------------------------------

func TestRunEmbedded_Real(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	res := runEmbedded(rep, embedCfg{tenants: 2, users: 1, conc: 2, dur: 120 * time.Millisecond, mix: mixChatOnly, label: "real-embedded"})
	if res.total == 0 {
		t.Fatalf("expected some real requests to complete, got total=0 (report: %s)", buf.String())
	}
}

func TestRunEmbeddedOnDisk_Real(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	res := runEmbeddedOnDisk(rep, embedCfg{tenants: 2, users: 1, conc: 2, dur: 120 * time.Millisecond, mix: mixChatOnly, label: "real-on-disk"})
	if res.total == 0 {
		t.Fatalf("expected some real requests to complete, got total=0 (report: %s)", buf.String())
	}
	if _, had := os.LookupEnv("AISOTROPY_DATA_DIR"); had {
		t.Fatal("AISOTROPY_DATA_DIR leaked out of runEmbeddedOnDisk")
	}
}

// TestRunEmbeddedOnDisk_RestoresPriorDataDir covers the "had == true" restore
// branch: when AISOTROPY_DATA_DIR was already set before the call, it must be
// put back afterwards instead of unset.
func TestRunEmbeddedOnDisk_RestoresPriorDataDir(t *testing.T) {
	priorDir := t.TempDir()
	t.Setenv("AISOTROPY_DATA_DIR", priorDir)

	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	res := runEmbeddedOnDisk(rep, embedCfg{tenants: 2, users: 1, conc: 2, dur: 100 * time.Millisecond, mix: mixChatOnly, label: "restore-check"})
	if res.total == 0 {
		t.Fatalf("expected some real requests to complete, got total=0 (report: %s)", buf.String())
	}
	if got := os.Getenv("AISOTROPY_DATA_DIR"); got != priorDir {
		t.Fatalf("AISOTROPY_DATA_DIR = %q after call, want restored value %q", got, priorDir)
	}
}

// TestRunEmbeddedOnDisk_MkdirTempFails covers the os.MkdirTemp failure branch
// by pointing TMPDIR at a path that does not exist.
func TestRunEmbeddedOnDisk_MkdirTempFails(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	res := runEmbeddedOnDisk(rep, embedCfg{tenants: 2, users: 1, conc: 2, dur: 50 * time.Millisecond, mix: mixChatOnly, label: "mkdir-fail"})
	if res.total != 0 {
		t.Fatalf("expected a zero-value result when tempdir creation fails, got total=%d", res.total)
	}
	if !strings.Contains(buf.String(), "tempdir FAILED") {
		t.Fatalf("expected 'tempdir FAILED' line, got: %s", buf.String())
	}
}

func TestRunQuotaGate_Real(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	runQuotaGate(rep, 120*time.Millisecond)
	if !strings.Contains(buf.String(), "VERDICT") {
		t.Fatalf("expected a VERDICT line, got: %s", buf.String())
	}
}

func TestRunIsolationUnderLoad_Real(t *testing.T) {
	var buf bytes.Buffer
	rep := &reporter{w: &buf}
	runIsolationUnderLoad(rep, 150*time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, "VERDICT") {
		t.Fatalf("expected a VERDICT line, got: %s", out)
	}
	if !strings.Contains(out, "cross-tenant leaks detected: 0") {
		t.Fatalf("expected zero real cross-tenant leaks, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// run() / main() orchestration
// ---------------------------------------------------------------------------

func TestRun_FullSweep(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "results.txt")
	args := []string{"-dur=40ms", "-maxconc=2", "-users=1", "-out=" + outPath}
	if err := run(args, io.Discard); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading results file: %v", err)
	}
	out := string(data)
	for _, want := range []string{
		"aisotropy FULL-RUNTIME multi-tenant LOAD TEST",
		"SCENARIO A:",
		"SCENARIO B:",
		"SCENARIO C:",
		"SCENARIO D:",
		"SCENARIO E:",
		"Finished:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results file missing %q", want)
		}
	}
}

func TestRun_RelativeOutPath(t *testing.T) {
	// When -out is relative, run() resolves it against os.Getwd() rather than
	// this source file's directory. t.Chdir (auto-restored) exercises that
	// branch without touching the real working directory.
	t.Chdir(t.TempDir())
	args := []string{"-dur=40ms", "-maxconc=2", "-users=1", "-out=relative-results.txt"}
	if err := run(args, io.Discard); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, "relative-results.txt")); err != nil {
		t.Fatalf("expected results file resolved against cwd: %v", err)
	}
}

func TestRun_BadFlag(t *testing.T) {
	if err := run([]string{"-no-such-flag"}, io.Discard); err == nil {
		t.Fatal("expected an error for an unknown flag, got nil")
	}
}

func TestRun_CannotCreateResultsFile(t *testing.T) {
	// A path under a nonexistent parent directory makes os.Create fail.
	bad := filepath.Join(t.TempDir(), "does-not-exist", "results.txt")
	err := run([]string{"-out=" + bad}, io.Discard)
	if err == nil {
		t.Fatal("expected an error creating the results file, got nil")
	}
	if !strings.Contains(err.Error(), "create results file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// sanity: httpDoer is satisfied by *http.Client, which is what production code
// relies on (bootApp returns *http.Client and passes it straight into
// driveLoad/driveQuotaGate/driveIsolationCheck).
var _ httpDoer = (*http.Client)(nil)
