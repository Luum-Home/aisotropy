//go:build proxy

// Package proxy_test is the executable proof that aisotropy's token-by-token SSE
// stream survives a reverse proxy end-to-end — through Caddy AND nginx — and that
// the proof has teeth: a deliberately-buffering nginx (the CONTROL) demonstrably
// dumps the whole stream at once, so the harness can tell streaming from buffering.
//
// It is gated behind the `proxy` build tag and skips cleanly (never a fake pass)
// when docker / docker compose are unavailable. Run it with:
//
//	go test -tags proxy ./test/proxy/ -v -timeout 300s
//
// or via ./run.sh. The test owns the whole lifecycle: `docker compose up --build`,
// readiness poll through each proxy, per-chunk timing capture, assertions, and
// `docker compose down -v` cleanup on exit (including on failure).
package proxy_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Host ports published by docker-compose.yml, one per fronting proxy.
const (
	caddyURL         = "http://127.0.0.1:18081"
	nginxURL         = "http://127.0.0.1:18082"
	nginxBufferedURL = "http://127.0.0.1:18083" // CONTROL: buffers on purpose
)

// streamPath is the token-by-token SSE route under test.
const streamPath = "/v1/tenant/t/user/u/chat/stream"

// Assertion thresholds. The stub LLM emits ~30 words at 15ms spacing (~450ms of
// wall time), so a streamed response arrives as many reads spread over hundreds
// of ms; a buffered one arrives as a single dump with a near-zero span.
const (
	minStreamReads = 3                      // a real stream delivers many incremental reads
	minStreamSpan  = 100 * time.Millisecond // first-to-last read must span real time
	maxBufferSpan  = 50 * time.Millisecond  // a buffered dump arrives all-at-once
)

// arrival records one Read() that returned bytes: when it landed (since request
// start) and how many bytes it carried.
type arrival struct {
	at    time.Duration
	bytes int
}

func TestSSESurvivesReverseProxies(t *testing.T) {
	requireDocker(t)

	composeUp(t)
	t.Cleanup(func() { composeDown(t) })

	// Wait until each proxy can reach aisotropyd's liveness endpoint. If a proxy
	// never comes up the sub-tests below fail loudly rather than hang.
	for _, p := range []struct{ name, url string }{
		{"caddy", caddyURL},
		{"nginx", nginxURL},
		{"nginx-buffered", nginxBufferedURL},
	} {
		if !waitReady(t, p.url, 60*time.Second) {
			dumpLogs(t)
			t.Fatalf("%s (%s) never became ready", p.name, p.url)
		}
	}

	// --- The two shipped configs MUST stream incrementally. ---
	t.Run("caddy_streams", func(t *testing.T) {
		assertStreams(t, caddyURL, "sess-caddy")
	})
	t.Run("nginx_streams", func(t *testing.T) {
		assertStreams(t, nginxURL, "sess-nginx")
	})

	// --- The CONTROL MUST buffer (proves the streaming assertion has teeth). ---
	t.Run("nginx_buffered_control_buffers", func(t *testing.T) {
		assertBuffers(t, nginxBufferedURL, "sess-control")
	})
}

// assertStreams drives the SSE endpoint through baseURL and requires incremental
// delivery: at least minStreamReads distinct byte-bearing reads whose first→last
// span exceeds minStreamSpan. That timing signature is the proof buffering is OFF
// end-to-end.
func assertStreams(t *testing.T, baseURL, session string) {
	t.Helper()
	// No Accept-Encoding: the shipped configs neither buffer nor gzip, so a plain
	// SSE reader sees the live token trickle.
	arrivals, body := streamOnce(t, baseURL, session, false)
	logTiming(t, baseURL, arrivals, body)

	if len(arrivals) < minStreamReads {
		t.Fatalf("%s: expected >= %d incremental reads, got %d — looks buffered, not streamed",
			baseURL, minStreamReads, len(arrivals))
	}
	span := arrivals[len(arrivals)-1].at - arrivals[0].at
	if span < minStreamSpan {
		t.Fatalf("%s: first→last read span %v < %v — arrived as one dump, not a live stream",
			baseURL, span, minStreamSpan)
	}
	if !strings.Contains(body, "event: token") {
		t.Fatalf("%s: response did not contain SSE token events; got:\n%s", baseURL, body)
	}
	t.Logf("%s STREAMS: %d reads over %v ✓", baseURL, len(arrivals), span)
}

// assertBuffers drives the SSE endpoint through the CONTROL proxy and requires
// the opposite: the whole body arrives all-at-once (near-zero first→last span),
// demonstrating that a buffering proxy that ignores X-Accel-Buffering breaks the
// stream — and therefore that the passing streams above are a real result.
func assertBuffers(t *testing.T, baseURL, session string) {
	t.Helper()
	// Send Accept-Encoding: gzip, exactly as every browser does. That is what
	// arms the control's gzip footgun: nginx compresses (and therefore fully
	// buffers) the event stream into a single dump. streamOnce transparently
	// gunzips the body so the content sanity-check below still works.
	arrivals, body := streamOnce(t, baseURL, session, true)
	logTiming(t, baseURL, arrivals, body)

	if len(arrivals) == 0 {
		t.Fatalf("%s: no bytes received at all", baseURL)
	}
	span := arrivals[len(arrivals)-1].at - arrivals[0].at
	if span > maxBufferSpan {
		t.Fatalf("%s: CONTROL was expected to BUFFER (span <= %v) but span was %v — "+
			"the control is not actually buffering, so the streaming assertions lack teeth",
			baseURL, maxBufferSpan, span)
	}
	// Sanity: the buffered dump must still carry the same stream content.
	if !strings.Contains(body, "event: token") {
		t.Fatalf("%s: buffered response did not contain SSE token events; got:\n%s", baseURL, body)
	}
	t.Logf("%s BUFFERS (control ✓): %d read(s) over %v — single dump as expected",
		baseURL, len(arrivals), span)
}

// streamOnce POSTs to the SSE endpoint through baseURL and reads the response
// body chunk by chunk, timestamping every byte-bearing Read relative to the
// request start. It returns the arrival timeline and the full body text.
func streamOnce(t *testing.T, baseURL, session string, acceptGzip bool) ([]arrival, string) {
	t.Helper()

	// A distinct session per proxy: the working-memory tier accumulates prior
	// turns into the system prompt, so sharing one session would let each
	// successive reply grow. Isolated sessions keep replies small and comparable.
	reqBody := []byte(fmt.Sprintf(
		`{"prompt":"stream through a reverse proxy please","product_id":"proxytest","session_id":%q}`,
		session))
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, baseURL+streamPath, bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if acceptGzip {
		req.Header.Set("Accept-Encoding", "gzip")
	}

	// A dedicated transport with no client-side response buffering, so the read
	// timeline reflects network arrival, not the client's own coalescing.
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableCompression:  true,
			DisableKeepAlives:   true,
			MaxIdleConns:        1,
			IdleConnTimeout:     time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s: request failed: %v", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: status %d, body: %s", baseURL, resp.StatusCode, string(b))
	}

	var arrivals []arrival
	var buf bytes.Buffer
	reader := bufio.NewReaderSize(resp.Body, 1) // tiny buffer: surface each network flush
	chunk := make([]byte, 4096)
	for {
		n, readErr := reader.Read(chunk)
		if n > 0 {
			arrivals = append(arrivals, arrival{at: time.Since(start), bytes: n})
			buf.Write(chunk[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			// A mid-stream transport error is a real failure of the run.
			t.Fatalf("%s: read error after %d arrivals: %v", baseURL, len(arrivals), readErr)
		}
	}

	// Timing came from the raw wire reads above. If the server compressed the
	// body (the control's gzip footgun), decompress it for the content check —
	// the decode does not affect the already-captured arrival timeline.
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("%s: response was gzip but failed to open gzip reader: %v", baseURL, err)
		}
		defer gz.Close()
		decoded, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("%s: failed to gunzip response body: %v", baseURL, err)
		}
		return arrivals, string(decoded)
	}
	return arrivals, buf.String()
}

func logTiming(t *testing.T, baseURL string, arrivals []arrival, body string) {
	t.Helper()
	var span time.Duration
	if len(arrivals) > 0 {
		span = arrivals[len(arrivals)-1].at - arrivals[0].at
	}
	t.Logf("%s: %d reads, %d body bytes, first→last span %v",
		baseURL, len(arrivals), len(body), span)
	// Compact per-read trace (helps when a numbers-based assertion fails).
	var b strings.Builder
	for i, a := range arrivals {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%v(+%dB)", a.at.Round(time.Millisecond), a.bytes)
	}
	t.Logf("%s arrivals: [%s]", baseURL, b.String())
}

// waitReady polls GET /healthz through baseURL until it answers 200 or times out.
func waitReady(t *testing.T, baseURL string, timeout time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// --- docker compose lifecycle ------------------------------------------------

func composeUp(t *testing.T) {
	t.Helper()
	t.Log("docker compose up --build (first run builds the aisotropyd image; may take a minute)...")
	out, err := compose(5*time.Minute, "up", "-d", "--build")
	if err != nil {
		t.Fatalf("docker compose up failed: %v\n%s", err, out)
	}
}

func composeDown(t *testing.T) {
	t.Helper()
	// -v drops volumes, --rmi local removes the aisotropyd image we built, so the
	// rig leaves nothing behind (containers, volumes, or images).
	out, err := compose(2*time.Minute, "down", "-v", "--remove-orphans", "--rmi", "local")
	if err != nil {
		t.Logf("docker compose down warning: %v\n%s", err, out)
	}
}

func dumpLogs(t *testing.T) {
	t.Helper()
	out, _ := compose(30*time.Second, "logs", "--no-color", "--tail", "50")
	t.Logf("compose logs (tail):\n%s", out)
}

// compose runs `docker compose <args...>` in the package directory (where
// docker-compose.yml lives — go test sets cwd to the package dir).
func compose(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"compose", "-f", "docker-compose.yml"}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// requireDocker skips (never fails) the test when docker or the compose plugin
// are unavailable — a clean SKIP, never a fake PASS.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH — skipping proxy integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not available — skipping proxy integration test:\n%s", string(out))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if out, err := exec.CommandContext(ctx2, "docker", "compose", "version").CombinedOutput(); err != nil {
		t.Skipf("docker compose plugin not available — skipping proxy integration test:\n%s", string(out))
	}
}
