// Command loadgen is the precision load generator for the streaming bake-off.
// Unlike k6/ghz (which measure at connection granularity), loadgen reads EVERY
// token frame with a timestamp, so it reports true TTFT and inter-token latency
// percentiles (p50/p95/p99) plus sustained tokens/s under N concurrent streams,
// identically across SSE(h1), SSE(h2), WebSocket, and gRPC. It also polls the
// server /stats endpoint for max-concurrent-streams and server heap.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"bufio"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "aisotropy.local/bench/streaming/proto/streampb"
)

func main() {
	transport := flag.String("transport", "sse-h1", "sse-h1|sse-h2|ws|grpc")
	host := flag.String("host", "127.0.0.1", "server host")
	n := flag.Int("n", 100, "concurrent streams (tenants)")
	count := flag.Int("count", 200, "tokens per stream")
	interval := flag.Int("interval", 5, "server inter-token interval (ms)")
	statsURL := flag.String("stats", "", "optional /stats URL to poll for server max-concurrent/heap")
	label := flag.String("label", "", "label for the result row (defaults to transport)")
	cancelAfter := flag.Int("cancel-after", 0, "if >0, cancel each client after this many ms (cancellation-correctness gate)")
	h1MaxConns := flag.Int("h1-max-conns", 0, "if >0, cap sse-h1 client connections per host (emulate the browser 6-per-origin cap)")
	flag.Parse()

	h1CapOverride = *h1MaxConns

	if *label == "" {
		*label = *transport
	}

	// Poll server stats in the background to capture the high-water mark.
	stopStats := make(chan struct{})
	var statsWG sync.WaitGroup
	var peak serverStats
	if *statsURL != "" {
		statsWG.Add(1)
		go func() {
			defer statsWG.Done()
			t := time.NewTicker(25 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopStats:
					return
				case <-t.C:
					if s, err := fetchStats(*statsURL); err == nil {
						if s.MaxActive > peak.MaxActive {
							peak.MaxActive = s.MaxActive
						}
						if s.HeapAllocMB > peak.HeapAllocMB {
							peak.HeapAllocMB = s.HeapAllocMB
						}
						if s.Goroutines > peak.Goroutines {
							peak.Goroutines = s.Goroutines
						}
						peak.StreamCancelled = s.StreamCancelled
						peak.StreamCompleted = s.StreamCompleted
					}
				}
			}
		}()
	}

	runFn, err := clientFor(*transport, *host)
	if err != nil {
		log.Fatal(err)
	}

	results := make([]streamResult, *n)
	var wg sync.WaitGroup
	wallStart := time.Now()
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := context.Background()
			if *cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(*cancelAfter)*time.Millisecond)
				defer cancel()
			}
			results[idx] = runFn(ctx, *count, *interval)
		}(i)
	}
	wg.Wait()
	wallDur := time.Since(wallStart)

	close(stopStats)
	statsWG.Wait()

	report(*label, *n, *count, results, wallDur, peak, *statsURL != "")
}

// streamResult holds the timing for one stream.
type streamResult struct {
	ok         bool
	err        error
	ttft       time.Duration   // request start -> first token
	interToken []time.Duration // gaps between successive tokens (client-observed)
	tokens     int
	dur        time.Duration // request start -> last token
}

// runFunc runs one stream and returns its result.
type runFunc func(ctx context.Context, count, intervalMS int) streamResult

func clientFor(transport, host string) (runFunc, error) {
	switch transport {
	case "sse-h1":
		return sseRunner(fmt.Sprintf("http://%s:18085/sse", host), h1Client()), nil
	case "sse-h2":
		return sseRunner(fmt.Sprintf("https://%s:18086/sse", host), h2Client()), nil
	case "ws":
		return wsRunner(fmt.Sprintf("ws://%s:18087/ws", host)), nil
	case "grpc":
		return grpcRunner(fmt.Sprintf("%s:18088", host)), nil
	case "wt":
		return wtRunner(fmt.Sprintf("https://%s:18089/wt", host)), nil
	default:
		return nil, fmt.Errorf("unknown transport %q", transport)
	}
}

// --- HTTP clients: one forces HTTP/1.1, the other forces HTTP/2 over TLS ---

// h1CapOverride, when >0, caps MaxConnsPerHost on the sse-h1 client to emulate a
// browser's per-origin connection limit (Chrome/Firefox use 6). With long-lived
// SSE streams this reproduces the real cap: once the pool is saturated, further
// streams cannot even open — the same starvation a 7th browser EventSource hits.
var h1CapOverride int

func h1Client() *http.Client {
	max := 0 // unlimited by default: measure the protocol, not an artificial cap
	if h1CapOverride > 0 {
		max = h1CapOverride
	}
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:   false,
			TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // disable h2
			MaxIdleConnsPerHost: 4096,
			MaxConnsPerHost:     max,
		},
	}
}

func h2Client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}},
		},
	}
}

func sseRunner(url string, client *http.Client) runFunc {
	return func(ctx context.Context, count, intervalMS int) streamResult {
		var res streamResult
		full := fmt.Sprintf("%s?count=%d&interval_ms=%d", url, count, intervalMS)
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		resp, err := client.Do(req)
		if err != nil {
			res.err = err
			return res
		}
		defer resp.Body.Close()
		return readFramed(start, resp.Body, '\n')
	}
}

// readFramed reads SSE "data: ...\n\n" frames token-by-token, timestamping each.
func readFramed(start time.Time, r io.Reader, _ byte) streamResult {
	var res streamResult
	buf := make([]byte, 4096)
	var last time.Time
	// SSE events are separated by a blank line; each token is one event here.
	// We count "data: " occurrences as tokens via a light scanner.
	var carry []byte
	for {
		nb, err := r.Read(buf)
		if nb > 0 {
			now := time.Now()
			carry = append(carry, buf[:nb]...)
			for {
				idx := indexDoubleNL(carry)
				if idx < 0 {
					break
				}
				// one event completed
				if res.tokens == 0 {
					res.ttft = now.Sub(start)
				} else {
					res.interToken = append(res.interToken, now.Sub(last))
				}
				last = now
				res.tokens++
				carry = carry[idx+2:]
			}
		}
		if err != nil {
			break
		}
	}
	res.dur = time.Since(start)
	res.ok = res.tokens > 0
	return res
}

func indexDoubleNL(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\n' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func wsRunner(url string) runFunc {
	return func(ctx context.Context, count, intervalMS int) streamResult {
		var res streamResult
		full := fmt.Sprintf("%s?count=%d&interval_ms=%d", url, count, intervalMS)
		start := time.Now()
		c, _, err := websocket.Dial(ctx, full, nil)
		if err != nil {
			res.err = err
			return res
		}
		defer c.CloseNow()
		c.SetReadLimit(-1)
		var last time.Time
		for {
			_, data, rerr := c.Read(ctx)
			if rerr != nil {
				break
			}
			now := time.Now()
			_ = data
			if res.tokens == 0 {
				res.ttft = now.Sub(start)
			} else {
				res.interToken = append(res.interToken, now.Sub(last))
			}
			last = now
			res.tokens++
		}
		res.dur = time.Since(start)
		res.ok = res.tokens > 0
		return res
	}
}

func grpcRunner(target string) runFunc {
	// One shared connection is idiomatic for gRPC (HTTP/2 multiplexes N streams
	// over it) — the opposite of SSE-h1 where each stream needs its own TCP conn.
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(pickCreds()))
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	client := pb.NewTokenStreamerClient(conn)
	return func(ctx context.Context, count, intervalMS int) streamResult {
		var res streamResult
		start := time.Now()
		stream, err := client.Stream(ctx, &pb.StreamRequest{
			TenantId: "t", UserId: "u",
			Count: int32(count), IntervalMs: int32(intervalMS),
		})
		if err != nil {
			res.err = err
			return res
		}
		var last time.Time
		for {
			_, rerr := stream.Recv()
			if rerr != nil {
				break
			}
			now := time.Now()
			if res.tokens == 0 {
				res.ttft = now.Sub(start)
			} else {
				res.interToken = append(res.interToken, now.Sub(last))
			}
			last = now
			res.tokens++
		}
		res.dur = time.Since(start)
		res.ok = res.tokens > 0
		return res
	}
}

func pickCreds() credentials.TransportCredentials { return insecure.NewCredentials() }

// wtRunner opens one WebTransport (HTTP/3 over QUIC) session per stream — the
// same one-connection-per-tenant model as WebSocket — and reads the server's
// unidirectional token stream. Each stream gets its own Dialer so QUIC sessions
// are independent (fair comparison to WS/SSE-h1).
func wtRunner(url string) runFunc {
	return func(ctx context.Context, count, intervalMS int) streamResult {
		var res streamResult
		d := &webtransport.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}},
			QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
		}
		start := time.Now()
		_, conn, err := d.Dial(ctx, url, nil)
		if err != nil {
			res.err = err
			return res
		}
		defer conn.CloseWithError(0, "done")
		s, err := conn.AcceptUniStream(ctx)
		if err != nil {
			res.err = err
			return res
		}
		r := bufio.NewReader(s)
		var last time.Time
		for {
			_, rerr := r.ReadString('\n')
			if rerr != nil {
				break
			}
			now := time.Now()
			if res.tokens == 0 {
				res.ttft = now.Sub(start)
			} else {
				res.interToken = append(res.interToken, now.Sub(last))
			}
			last = now
			res.tokens++
		}
		res.dur = time.Since(start)
		res.ok = res.tokens > 0
		return res
	}
}

// --- server /stats polling ---

type serverStats struct {
	Goroutines      int   `json:"goroutines"`
	HeapAllocMB     int   `json:"heap_alloc_mb"`
	MaxActive       int64 `json:"max_active"`
	StreamCancelled int64 `json:"stream_cancelled"`
	StreamCompleted int64 `json:"stream_completed"`
}

func fetchStats(url string) (serverStats, error) {
	var s serverStats
	resp, err := http.Get(url)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// --- reporting ---

func report(label string, n, count int, results []streamResult, wall time.Duration, peak serverStats, hadStats bool) {
	var ttfts []time.Duration
	var inter []time.Duration
	okCount, totalTokens := 0, 0
	var firstErr error
	for _, r := range results {
		if r.ok {
			okCount++
			ttfts = append(ttfts, r.ttft)
			inter = append(inter, r.interToken...)
			totalTokens += r.tokens
		} else if firstErr == nil {
			firstErr = r.err
		}
	}
	sustained := float64(totalTokens) / wall.Seconds()

	fmt.Printf("=== %s | n=%d count=%d ===\n", label, n, count)
	fmt.Printf("streams ok         : %d/%d\n", okCount, n)
	if firstErr != nil {
		fmt.Printf("first error        : %v\n", firstErr)
	}
	fmt.Printf("wall time          : %s\n", wall.Round(time.Millisecond))
	fmt.Printf("tokens delivered   : %d\n", totalTokens)
	fmt.Printf("sustained tokens/s : %.0f\n", sustained)
	fmt.Printf("TTFT      p50/p95/p99 : %s / %s / %s\n", pctl(ttfts, 50), pctl(ttfts, 95), pctl(ttfts, 99))
	fmt.Printf("inter-tok p50/p95/p99 : %s / %s / %s (target %dms)\n",
		pctl(inter, 50), pctl(inter, 95), pctl(inter, 99), 5)
	if hadStats {
		fmt.Printf("server max concurrent : %d\n", peak.MaxActive)
		fmt.Printf("server peak heap (MB) : %d\n", peak.HeapAllocMB)
		fmt.Printf("server peak goroutines: %d\n", peak.Goroutines)
		fmt.Printf("server cancelled/compl: %d / %d\n", peak.StreamCancelled, peak.StreamCompleted)
	}
	fmt.Println()
	_ = os.Stdout.Sync()
}

func pctl(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := make([]time.Duration, len(d))
	copy(cp, d)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := (p * (len(cp) - 1)) / 100
	return cp[idx].Round(10 * time.Microsecond)
}
