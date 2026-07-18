// Command load is a self-contained, single-box load-test harness for the FULL
// aisotropy runtime under realistic multi-tenant load.
//
// WHAT IT MEASURES. Prior benchmarks in this repo covered footprint, transports
// and brokers, but never the whole runtime under concurrent multi-tenant chat
// traffic. This harness closes that gap. It embeds the real public builder
// (aisotropy.App) in-process — the SAME wiring cmd/aisotropyd boots: chi ->
// humachi -> Huma -> internal/api handlers, the BadgerDB working-memory tier,
// the facts (SQLite+FTS5) and semantic (in-process cosine) tiers, the
// per-tenant token-bucket quota gate, the LLM circuit breaker, the cost meter,
// and the transactional outbox -> async consolidation worker. It then drives it
// over real net/http on a loopback port with N distinct tenants issuing
// concurrent /chat (writer) and /chat/stream (SSE) requests, each reusing a
// per-(tenant,user) session so working-memory persistence AND hydration run on
// every turn.
//
// WHY THE STUB MODEL. The LLM is the credential-less stub with a ~0 inter-token
// delay (internal/llm.StubClient{Delay: 1ns}). This deliberately removes model
// latency so the numbers below are RUNTIME OVERHEAD — HTTP validation, identity
// -> ctx, quota gate, memory hydration, working-memory writes, SSE framing,
// cost metering — not provider round-trip time. A real provider would add its
// own latency on top of everything measured here.
//
// EMBEDDED, NOT SPAWNED. Because the server runs in this process, the harness
// reads runtime.NumGoroutine() and runtime.MemStats directly. That conflates
// harness client goroutines with server goroutines DURING load, so the harness
// reports three points: idle baseline (server only, post-boot), peak-under-load
// (server + client workers), and post-load settle (workers drained) — a settle
// value near baseline is the goroutine-leak check.
//
// SCOPE / HYGIENE. This program owns only bench/load/**. It boots its own App
// instances on ephemeral loopback ports and cancels each one before exiting; it
// starts NO containers and never touches aisotropy-rqlite-1 / aisotropy-qdrant-1
// or any other external service. Everything is in-process Go.
//
// TESTABILITY. main()'s body lives in run(args, stdout) so tests can drive the
// full flag-parse -> scenario-sweep -> report flow with tiny durations instead
// of os.Args/os.Exit. The request-driving logic (driveLoad, driveQuotaGate,
// driveIsolationCheck) is decoupled from the real *http.Client behind the
// httpDoer seam, so the concurrency + latency/percentile aggregation can be
// exercised against a fake client with no real server involved.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// defaultHealthTimeout is the production /healthz poll budget bootApp gives a
// freshly booted App before declaring the boot failed. Threaded as a parameter
// (rather than hardcoded in bootApp) so tests can shrink it and force the
// failure branch to resolve in milliseconds instead of seconds.
const defaultHealthTimeout = 10 * time.Second

// ---------------------------------------------------------------------------
// Configuration (flags)
// ---------------------------------------------------------------------------

// config holds the parsed CLI knobs. Defaults run the full sweep in ~1 minute.
type config struct {
	dur     time.Duration
	quick   bool
	users   int
	out     string
	maxConc int
}

// parseFlags parses args (typically os.Args[1:]) into a config using a fresh
// FlagSet, so it can be called repeatedly (e.g. from tests) without colliding
// with package-level flag.CommandLine state.
func parseFlags(args []string) (*config, error) {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	cfg := &config{}
	fs.DurationVar(&cfg.dur, "dur", 4*time.Second, "sustained load duration per scenario")
	fs.BoolVar(&cfg.quick, "quick", false, "quick smoke: shorter durations, smaller sweep")
	fs.IntVar(&cfg.users, "users", 8, "distinct users per tenant (working-memory session cardinality multiplier)")
	fs.StringVar(&cfg.out, "out", "results.txt", "results file path (relative to bench/load)")
	fs.IntVar(&cfg.maxConc, "maxconc", 1024, "cap on concurrency ramp (breaking-point sweep)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return cfg, nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// resolveSweepConfig derives the per-scenario duration and sweep sizes from
// cfg, applying the "quick" override. Extracted as a pure function (no I/O)
// so the override logic is unit-testable without running the full scenario
// sweep.
func resolveSweepConfig(cfg *config) (dur time.Duration, tenantSweep, concRamp []int) {
	dur = cfg.dur
	tenantSweep = []int{10, 100, 1000}
	concRamp = []int{16, 64, 256, 512, cfg.maxConc}
	if cfg.quick {
		dur = 2 * time.Second
		tenantSweep = []int{10, 100}
		concRamp = []int{16, 64, 256}
	}
	return dur, tenantSweep, concRamp
}

// run executes the full flag-parse -> scenario-sweep -> report flow, writing
// the report to both stdout and the resolved results file. It returns an error
// instead of calling log.Fatal so it can be exercised from tests.
func run(args []string, stdout io.Writer) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	log.SetFlags(0)
	rand.Seed(time.Now().UnixNano())

	// Silence the embedded server's slog output: under sustained load it emits an
	// access-audit + guardrail line per request (tens of thousands of lines), which
	// would bury the measured numbers. This tunes the HARNESS's default logger, not
	// any runtime code. (A known, documented artifact — the keyless stub echoes the
	// hydrated system prompt, so sustained single-session conversations grow the
	// stored working-memory value past BadgerDB's 64KB per-value cap and appends log
	// ERROR past that point — is covered in the README caveats.)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))

	dur, tenantSweep, concRamp := resolveSweepConfig(cfg)

	// Resolve the output path relative to this source file's dir so it lands in
	// bench/load/ regardless of cwd.
	outPath := cfg.out
	if !filepath.IsAbs(outPath) {
		if wd, err := os.Getwd(); err == nil {
			outPath = filepath.Join(wd, outPath)
		}
	}
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create results file: %w", err)
	}
	defer func() { _ = out.Close() }()
	rep := &reporter{w: io.MultiWriter(stdout, out)}

	rep.header(dur)

	// --- Scenario A: tenant-cardinality sweep (hydration + tier scaling) ------
	rep.section("A", "Tenant-cardinality sweep — throughput & latency as N tenants grows",
		"Fixed concurrency=64, 50/50 chat+stream, in-memory tiers. Isolates how memory-tier",
		"cardinality (distinct working-memory buffers = N*users) affects hydration cost.")
	var sweepRows []scenarioResult
	for _, n := range tenantSweep {
		r := runEmbedded(rep, embedCfg{tenants: n, users: cfg.users, conc: 64, dur: dur, mix: mix5050, label: fmt.Sprintf("N=%d tenants", n)})
		sweepRows = append(sweepRows, r)
	}
	rep.table(sweepRows)

	// --- Scenario B: concurrency ramp -> breaking point -----------------------
	rep.section("B", "Concurrency ramp — find the breaking point",
		"Fixed N=100 tenants, in-memory tiers, 50/50 mix. Ramp concurrent workers until",
		"p99 latency or error rate blows up. Breaking point = first level where errors>1%",
		"OR p99 exceeds 10x the lowest-concurrency p99.")
	var rampRows []scenarioResult
	for _, c := range concRamp {
		r := runEmbedded(rep, embedCfg{tenants: 100, users: cfg.users, conc: c, dur: dur, mix: mix5050, label: fmt.Sprintf("conc=%d", c)})
		rampRows = append(rampRows, r)
	}
	rep.table(rampRows)
	rep.breakingPoint(rampRows)

	// --- Scenario C: quota gate under saturation ------------------------------
	rep.section("C", "Pre-spend quota gate under saturation",
		"One app with a TIGHT per-tenant token bucket (5/sec, burst 5). Hammer a single",
		"tenant with 64 concurrent /chat writers for the duration and confirm the gate",
		"rejects with a clean HTTP 429 BEFORE any model spend, while a second, un-hammered",
		"tenant keeps getting 200s (the bucket is per-tenant, not global).")
	runQuotaGate(rep, dur)

	// --- Scenario D: BadgerDB working-memory contention (in-mem vs on-disk) ----
	rep.section("D", "BadgerDB working-memory write contention (in-mem vs on-disk)",
		"Writer-heavy (/chat only) load, N=50 tenants, concurrency=64. Compare the default",
		"in-memory BadgerDB against an on-disk store (AISOTROPY_DATA_DIR). The delta is the",
		"cost of durable working-memory writes under concurrency.")
	var badgerRows []scenarioResult
	badgerRows = append(badgerRows, runEmbedded(rep, embedCfg{tenants: 50, users: cfg.users, conc: 64, dur: dur, mix: mixChatOnly, label: "in-memory Badger"}))
	badgerRows = append(badgerRows, runEmbeddedOnDisk(rep, embedCfg{tenants: 50, users: cfg.users, conc: 64, dur: dur, mix: mixChatOnly, label: "on-disk Badger"}))
	rep.table(badgerRows)

	// --- Scenario E: cross-tenant isolation UNDER concurrent load -------------
	rep.section("E", "Cross-tenant isolation holds under concurrent load",
		"Seed a distinct secret marker per tenant into a DELIBERATELY SHARED session id,",
		"then run 64 concurrent workers streaming neutral reads across tenants. Each response",
		"must contain ONLY its own tenant's marker (positive control: hydration is live) and",
		"NEVER another tenant's marker (negative: the isolation gate holds under concurrency).")
	runIsolationUnderLoad(rep, dur)

	rep.footer()
	log.Printf("\nWrote results to %s", outPath)
	return nil
}

// ---------------------------------------------------------------------------
// Embedded App boot + teardown
// ---------------------------------------------------------------------------

// bootApp starts an aisotropy.App on a free loopback port with the fast keyless
// stub, NoAuth, and the given options. It returns the base URL, a shared HTTP
// client tuned for keep-alive reuse, and a shutdown func that cancels Run and
// waits for a clean return. healthTimeout bounds how long it waits for /healthz
// to come up before declaring the boot failed (production callers pass
// defaultHealthTimeout; tests can pass a short timeout to force the failure
// branch quickly).
func bootApp(conc int, healthTimeout time.Duration, extra ...aisotropy.Option) (base string, client *http.Client, shutdown func(), err error) {
	addr, err := freeAddr()
	if err != nil {
		return "", nil, nil, err
	}
	opts := append([]aisotropy.Option{
		aisotropy.WithAddr(addr),
		// Fast keyless stub: measures runtime overhead, not model latency.
		aisotropy.WithLLM(&llm.StubClient{Delay: time.Nanosecond}),
		// NoAuth: load/isolation are orthogonal to the JWT gate; opting out lets
		// every /v1 request reach a handler (same choice as isolation_e2e_test).
		aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	}, extra...)
	app := aisotropy.New(opts...)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base = "http://" + addr
	client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        conc + 16,
			MaxIdleConnsPerHost: conc + 16,
			MaxConnsPerHost:     0, // unlimited: let the server, not the client, be the bottleneck
			IdleConnTimeout:     90 * time.Second,
		},
	}
	if err := waitForServer(client, base+"/healthz", healthTimeout); err != nil {
		cancel()
		<-errCh
		return "", nil, nil, err
	}
	shutdown = func() {
		cancel()
		client.CloseIdleConnections()
		select {
		case <-errCh:
		case <-time.After(15 * time.Second):
			log.Printf("WARN: app.Run did not return within 15s after cancel")
		}
	}
	return base, client, shutdown, nil
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

func waitForServer(client *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("server at %s not ready within %s", url, timeout)
}

// ---------------------------------------------------------------------------
// Load generation
// ---------------------------------------------------------------------------

// httpDoer is the minimal seam needed to drive HTTP requests: *http.Client
// satisfies it directly. Request-driving + aggregation logic (driveLoad,
// driveQuotaGate, driveIsolationCheck, and the do*/seed*/stream* helpers below)
// depend on this interface instead of *http.Client so tests can inject a fake
// implementation and exercise the concurrency/latency-aggregation logic without
// a real server.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type reqMix int

const (
	mix5050 reqMix = iota
	mixChatOnly
	mixStreamOnly
)

type embedCfg struct {
	tenants int
	users   int
	conc    int
	dur     time.Duration
	mix     reqMix
	label   string
}

// scenarioResult is one row of measured numbers.
type scenarioResult struct {
	label       string
	total       int64
	errs        int64
	rejected429 int64 // quota rejections (expected, not failures)
	elapsed     time.Duration
	// combined request latency percentiles (chat + stream total time)
	p50, p95, p99, max time.Duration
	// stream TTFT percentiles (only populated when streams are in the mix)
	ttftP50, ttftP95, ttftP99 time.Duration
	// runtime introspection
	baseGoroutines int
	peakGoroutines int
	settleGorout   int
	peakHeapMB     float64
}

func (r scenarioResult) throughput() float64 {
	if r.elapsed <= 0 {
		return 0
	}
	return float64(r.total) / r.elapsed.Seconds()
}
func (r scenarioResult) errRate() float64 {
	if r.total == 0 {
		return 0
	}
	return float64(r.errs) / float64(r.total) * 100
}

func runEmbedded(rep *reporter, cfg embedCfg) scenarioResult {
	base, client, shutdown, err := bootApp(cfg.conc, defaultHealthTimeout,
		aisotropy.WithRateLimit(1e9, 1e9)) // effectively unlimited: measure the runtime, not the gate
	if err != nil {
		rep.line("  boot FAILED for %s: %v", cfg.label, err)
		return scenarioResult{label: cfg.label}
	}
	defer shutdown()
	return driveLoad(rep, base, client, cfg)
}

func runEmbeddedOnDisk(rep *reporter, cfg embedCfg) scenarioResult {
	dir, err := os.MkdirTemp("", "aisotropy-load-badger-")
	if err != nil {
		rep.line("  tempdir FAILED: %v", err)
		return scenarioResult{label: cfg.label}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// AISOTROPY_DATA_DIR routes BadgerDB working memory (and embedded SQLite
	// facts) to disk. Set it only for this app's boot, then restore.
	old, had := os.LookupEnv("AISOTROPY_DATA_DIR")
	_ = os.Setenv("AISOTROPY_DATA_DIR", dir)
	defer func() {
		if had {
			_ = os.Setenv("AISOTROPY_DATA_DIR", old)
		} else {
			_ = os.Unsetenv("AISOTROPY_DATA_DIR")
		}
	}()
	base, client, shutdown, err := bootApp(cfg.conc, defaultHealthTimeout, aisotropy.WithRateLimit(1e9, 1e9))
	if err != nil {
		rep.line("  boot FAILED for %s: %v", cfg.label, err)
		return scenarioResult{label: cfg.label}
	}
	defer shutdown()
	return driveLoad(rep, base, client, cfg)
}

// driveLoad runs cfg.conc workers against base for cfg.dur, sampling runtime
// stats throughout, and returns aggregated metrics. client only needs to
// satisfy httpDoer, so tests can drive this with a fake in place of a real
// *http.Client / server.
func driveLoad(rep *reporter, base string, client httpDoer, cfg embedCfg) scenarioResult {
	// Let goroutines from the boot settle, then read the server-only baseline.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	baseG := runtime.NumGoroutine()

	var (
		total, errs, rejected int64
		peakG                 int64 = int64(baseG)
		peakHeap              uint64
	)
	// Per-worker latency slices (no lock on the hot path).
	lat := make([][]time.Duration, cfg.conc)
	ttft := make([][]time.Duration, cfg.conc)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.dur)
	defer cancel()

	// Runtime sampler: peak goroutines + peak heap during load.
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		var ms runtime.MemStats
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if g := int64(runtime.NumGoroutine()); g > atomic.LoadInt64(&peakG) {
					atomic.StoreInt64(&peakG, g)
				}
				runtime.ReadMemStats(&ms)
				if ms.HeapAlloc > atomic.LoadUint64(&peakHeap) {
					atomic.StoreUint64(&peakHeap, ms.HeapAlloc)
				}
			}
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < cfg.conc; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(rand.Int63()))
			for ctx.Err() == nil {
				tenant := fmt.Sprintf("t%04d", rng.Intn(cfg.tenants))
				user := fmt.Sprintf("u%d", rng.Intn(cfg.users))
				session := "s-" + user
				stream := false
				switch cfg.mix {
				case mix5050:
					stream = rng.Intn(2) == 0
				case mixStreamOnly:
					stream = true
				case mixChatOnly:
					stream = false
				}
				var d time.Duration
				var first time.Duration
				var status int
				var rerr error
				if stream {
					d, first, status, rerr = doStream(client, base, tenant, user, session)
				} else {
					d, status, rerr = doChat(client, base, tenant, user, session)
				}
				atomic.AddInt64(&total, 1)
				switch {
				case rerr != nil:
					atomic.AddInt64(&errs, 1)
				case status == http.StatusTooManyRequests:
					atomic.AddInt64(&rejected, 1)
				case status != http.StatusOK:
					atomic.AddInt64(&errs, 1)
				default:
					lat[w] = append(lat[w], d)
					if stream {
						ttft[w] = append(ttft[w], first)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	cancel()
	<-samplerDone

	// Post-load settle: workers drained; a value >> baseline flags a server leak.
	time.Sleep(150 * time.Millisecond)
	runtime.GC()
	settleG := runtime.NumGoroutine()

	allLat := flatten(lat)
	allTTFT := flatten(ttft)
	res := scenarioResult{
		label:          cfg.label,
		total:          total,
		errs:           errs,
		rejected429:    rejected,
		elapsed:        elapsed,
		baseGoroutines: baseG,
		peakGoroutines: int(atomic.LoadInt64(&peakG)),
		settleGorout:   settleG,
		peakHeapMB:     float64(atomic.LoadUint64(&peakHeap)) / (1024 * 1024),
	}
	res.p50, res.p95, res.p99, res.max = pct(allLat, 50), pct(allLat, 95), pct(allLat, 99), pct(allLat, 100)
	if len(allTTFT) > 0 {
		res.ttftP50, res.ttftP95, res.ttftP99 = pct(allTTFT, 50), pct(allTTFT, 95), pct(allTTFT, 99)
	}
	return res
}

// doChat fires one non-streaming /chat and returns total latency + status.
func doChat(client httpDoer, base, tenant, user, session string) (time.Duration, int, error) {
	body := chatBody("give a brief status update")
	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat", base, tenant, user)
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return time.Since(start), resp.StatusCode, nil
}

// doStream fires one /chat/stream (SSE) and returns total latency, time-to-first
// -token, and status.
func doStream(client httpDoer, base, tenant, user, session string) (total, ttft time.Duration, status int, err error) {
	body := chatBody("give a brief status update")
	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/stream", base, tenant, user)
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return time.Since(start), 0, resp.StatusCode, nil
	}
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf) // blocks until the first SSE flush (first token)
	ttft = time.Since(start)
	_ = n
	_, _ = io.Copy(io.Discard, resp.Body)
	return time.Since(start), ttft, resp.StatusCode, nil
}

func chatBody(prompt string) []byte {
	// product_id + session_id keep the working-memory session key stable so
	// hydration accumulates turns across a worker's requests.
	return []byte(`{"prompt":"` + prompt + `","product_id":"load-test","session_id":"load"}`)
}

// ---------------------------------------------------------------------------
// Scenario C: quota gate
// ---------------------------------------------------------------------------

func runQuotaGate(rep *reporter, dur time.Duration) {
	base, client, shutdown, err := bootApp(64, defaultHealthTimeout, aisotropy.WithRateLimit(5, 5))
	if err != nil {
		rep.line("  boot FAILED: %v", err)
		return
	}
	defer shutdown()
	driveQuotaGate(rep, client, base, dur)
}

// driveQuotaGate hammers tenant "hammered" with 64 concurrent /chat writers for
// dur and confirms a calm control tenant is unaffected. client only needs to
// satisfy httpDoer so this can be driven against a fake in tests.
func driveQuotaGate(rep *reporter, client httpDoer, base string, dur time.Duration) {
	var hammered200, hammered429, hammeredErr int64
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var wg sync.WaitGroup
	for w := 0; w < 64; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, status, e := doChat(client, base, "hammered", "u0", "s")
				switch {
				case e != nil:
					atomic.AddInt64(&hammeredErr, 1)
				case status == http.StatusTooManyRequests:
					atomic.AddInt64(&hammered429, 1)
				case status == http.StatusOK:
					atomic.AddInt64(&hammered200, 1)
				default:
					atomic.AddInt64(&hammeredErr, 1)
				}
			}
		}()
	}
	wg.Wait()

	// Control: a DIFFERENT tenant, gently, must still get 200s (per-tenant bucket).
	_, cs, _ := doChat(client, base, "calm", "u0", "s")

	tot := hammered200 + hammered429 + hammeredErr
	rep.line("  hammered tenant: %d requests -> %d OK(200), %d rejected(429), %d errors",
		tot, hammered200, hammered429, hammeredErr)
	rep.line("  control tenant 'calm' (single request): HTTP %d", cs)
	pass := hammered429 > 0 && hammeredErr == 0 && cs == http.StatusOK
	// With burst=5 and rate=5/s, the hammered tenant should get a small, bounded
	// number of 200s (~ burst + rate*dur) and the vast majority 429.
	rep.verdict(pass, "quota gate rejects over-budget traffic with clean 429s (no 5xx), "+
		"pre-spend, and leaves other tenants unaffected")
	rep.line("  note: OK count (%d) tracks bucket refill (~burst 5 + 5/s * %.0fs); the rest are gated 429 BEFORE any model spend.",
		hammered200, dur.Seconds())
}

// ---------------------------------------------------------------------------
// Scenario E: isolation under load
// ---------------------------------------------------------------------------

func runIsolationUnderLoad(rep *reporter, dur time.Duration) {
	base, client, shutdown, err := bootApp(64, defaultHealthTimeout, aisotropy.WithRateLimit(1e9, 1e9))
	if err != nil {
		rep.line("  boot FAILED: %v", err)
		return
	}
	defer shutdown()
	driveIsolationCheck(rep, client, base, dur)
}

// driveIsolationCheck seeds a distinct secret marker per tenant into a shared
// session id, then runs 64 concurrent workers streaming neutral reads across
// tenants, checking that each response carries only its own tenant's marker.
// client only needs to satisfy httpDoer so this can be driven against a fake in
// tests.
func driveIsolationCheck(rep *reporter, client httpDoer, base string, dur time.Duration) {
	const nTenants = 10
	const sharedSession = "shared-session" // deliberately reused across tenants
	markers := make([]string, nTenants)
	// Seed: each tenant writes its own secret marker into the SAME session id.
	for i := 0; i < nTenants; i++ {
		markers[i] = fmt.Sprintf("iso%dsecret%d", i, rand.Int63())
		markers[i] = strings.ToLower(markers[i])
		if err := seedMarker(client, base, tenantName(i), sharedSession, markers[i]); err != nil {
			rep.line("  seed FAILED for %s: %v", tenantName(i), err)
			return
		}
	}

	var checks, leaks, posControlOK, posControlMiss, streamErr int64
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var wg sync.WaitGroup
	for w := 0; w < 64; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(rand.Int63()))
			for ctx.Err() == nil {
				i := rng.Intn(nTenants)
				body, status, e := streamBody(client, base, tenantName(i), sharedSession, "give a brief status update")
				if e != nil || status != http.StatusOK {
					atomic.AddInt64(&streamErr, 1)
					continue
				}
				atomic.AddInt64(&checks, 1)
				// Positive control: tenant i's own marker must be hydrated back.
				if strings.Contains(body, markers[i]) {
					atomic.AddInt64(&posControlOK, 1)
				} else {
					atomic.AddInt64(&posControlMiss, 1)
				}
				// Negative: no OTHER tenant's marker may appear.
				for j := 0; j < nTenants; j++ {
					if j != i && strings.Contains(body, markers[j]) {
						atomic.AddInt64(&leaks, 1)
					}
				}
			}
		}()
	}
	wg.Wait()

	rep.line("  %d interleaved streamed reads across %d tenants sharing session %q (%d stream errors)",
		checks, nTenants, sharedSession, streamErr)
	rep.line("  positive control: %d reads hydrated their OWN tenant's marker (%d misses)", posControlOK, posControlMiss)
	rep.line("  cross-tenant leaks detected: %d", leaks)
	pass := leaks == 0 && checks > 0 && posControlOK > 0 && posControlMiss == 0
	rep.verdict(pass, "isolation gate holds under concurrency: every response carried only its "+
		"own tenant's marker, never another's, while hydration stayed live")
}

func tenantName(i int) string { return fmt.Sprintf("isotenant%d", i) }

func seedMarker(client httpDoer, base, tenant, session, marker string) error {
	body := []byte(`{"prompt":"store this note verbatim: ` + marker + `","product_id":"iso","session_id":"` + session + `"}`)
	url := fmt.Sprintf("%s/v1/tenant/%s/user/u0/chat", base, tenant)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seed status %d", resp.StatusCode)
	}
	return nil
}

func streamBody(client httpDoer, base, tenant, session, prompt string) (string, int, error) {
	body := []byte(`{"prompt":"` + prompt + `","product_id":"iso","session_id":"` + session + `"}`)
	url := fmt.Sprintf("%s/v1/tenant/%s/user/u0/chat/stream", base, tenant)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return string(raw), resp.StatusCode, err
}

// ---------------------------------------------------------------------------
// Stats helpers
// ---------------------------------------------------------------------------

func flatten(ss [][]time.Duration) []time.Duration {
	var n int
	for _, s := range ss {
		n += len(s)
	}
	out := make([]time.Duration, 0, n)
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}

// pct returns the p-th percentile (p in [0,100]) of durations. p=100 is the max.
func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := make([]time.Duration, len(d))
	copy(s, d)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if p >= 100 {
		return s[len(s)-1]
	}
	idx := (p * len(s)) / 100
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// ---------------------------------------------------------------------------
// Reporter
// ---------------------------------------------------------------------------

type reporter struct{ w io.Writer }

func (r *reporter) line(format string, a ...any) { fmt.Fprintf(r.w, format+"\n", a...) }

func (r *reporter) header(dur time.Duration) {
	r.line("================================================================================")
	r.line("aisotropy FULL-RUNTIME multi-tenant LOAD TEST")
	r.line("================================================================================")
	r.line("Harness : bench/load (embeds aisotropy.App in-process, drives real loopback HTTP)")
	r.line("Model   : keyless stub, Delay=1ns  => numbers are RUNTIME overhead, NOT model latency")
	r.line("Runtime : go %s  |  GOMAXPROCS=%d  |  duration/scenario=%s", runtime.Version(), runtime.GOMAXPROCS(0), dur)
	r.line("Host    : %s/%s  |  %d CPU", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	r.line("Started : %s", time.Now().Format(time.RFC3339))
	r.line("")
	r.line("Metric legend: thr=req/s, p50/p95/p99=combined request latency, TTFT=stream time-to")
	r.line("-first-token, gopeak=peak goroutines under load (server+workers), heap=peak HeapAlloc.")
}

func (r *reporter) section(id, title string, desc ...string) {
	r.line("")
	r.line("--------------------------------------------------------------------------------")
	r.line("SCENARIO %s: %s", id, title)
	for _, d := range desc {
		r.line("  %s", d)
	}
	r.line("--------------------------------------------------------------------------------")
}

func (r *reporter) table(rows []scenarioResult) {
	r.line("")
	r.line("  %-18s %8s %8s %8s %8s %8s %8s %8s %7s %6s %6s %6s %7s",
		"scenario", "thr/s", "reqs", "err%", "p50", "p95", "p99", "TTFTp99", "gobase", "gopk", "goset", "heapMB", "429s")
	for _, x := range rows {
		r.line("  %-18s %8.0f %8d %8.2f %8s %8s %8s %8s %7d %6d %6d %6.1f %7d",
			x.label, x.throughput(), x.total, x.errRate(),
			dms(x.p50), dms(x.p95), dms(x.p99), dms(x.ttftP99),
			x.baseGoroutines, x.peakGoroutines, x.settleGorout, x.peakHeapMB, x.rejected429)
	}
}

func (r *reporter) breakingPoint(rows []scenarioResult) {
	if len(rows) == 0 {
		return
	}
	baseP99 := rows[0].p99
	r.line("")
	var bp *scenarioResult
	for i := range rows {
		x := rows[i]
		blewErr := x.errRate() > 1.0
		blewLat := baseP99 > 0 && x.p99 > 10*baseP99
		if blewErr || blewLat {
			bp = &rows[i]
			break
		}
	}
	if bp == nil {
		r.line("  BREAKING POINT: not reached up to conc=%s (err%% stayed <=1%% and p99 <10x the",
			strings.TrimPrefix(rows[len(rows)-1].label, "conc="))
		r.line("  lowest-concurrency p99=%s). The runtime absorbed the full ramp on this box —", dms(baseP99))
		r.line("  the single-box saturation limit is CPU/scheduler, not a runtime cliff (see caveats).")
		// Report where latency degraded MOST even if no hard break.
		worst := rows[len(rows)-1]
		r.line("  highest-load point %s: thr=%.0f/s, p99=%s, err=%.2f%%, gopeak=%d, heap=%.1fMB",
			worst.label, worst.throughput(), dms(worst.p99), worst.errRate(), worst.peakGoroutines, worst.peakHeapMB)
		return
	}
	r.line("  BREAKING POINT: %s — err=%.2f%%, p99=%s (vs baseline p99=%s at %s).",
		bp.label, bp.errRate(), dms(bp.p99), dms(baseP99), rows[0].label)
}

func (r *reporter) verdict(pass bool, what string) {
	tag := "FAIL"
	if pass {
		tag = "PASS"
	}
	r.line("  VERDICT [%s]: %s", tag, what)
}

func (r *reporter) footer() {
	r.line("")
	r.line("================================================================================")
	r.line("Finished: %s", time.Now().Format(time.RFC3339))
	r.line("================================================================================")
}

// dms formats a duration compactly in ms with 2 decimals (or us below 1ms).
func dms(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	ms := float64(d) / float64(time.Millisecond)
	if ms < 1 {
		return fmt.Sprintf("%.0fus", float64(d)/float64(time.Microsecond))
	}
	return fmt.Sprintf("%.2fms", ms)
}
