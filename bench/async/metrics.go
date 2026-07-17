package asyncbench

import (
	"sort"
	"sync"
	"time"
)

// Latencies is a concurrency-safe collector of end-to-end latencies (one sample
// per consumed+acked envelope: consumer-ack-time minus relay-enqueue-time).
type Latencies struct {
	mu      sync.Mutex
	samples []time.Duration
}

func NewLatencies() *Latencies { return &Latencies{} }

func (l *Latencies) Add(d time.Duration) {
	l.mu.Lock()
	l.samples = append(l.samples, d)
	l.mu.Unlock()
}

func (l *Latencies) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.samples)
}

// Percentiles returns p50/p95/p99 (and max) over the collected samples. Returns
// zeroes if no samples.
func (l *Latencies) Percentiles() (p50, p95, p99, max time.Duration) {
	l.mu.Lock()
	s := make([]time.Duration, len(l.samples))
	copy(s, l.samples)
	l.mu.Unlock()
	if len(s) == 0 {
		return 0, 0, 0, 0
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	pick := func(p float64) time.Duration {
		if len(s) == 1 {
			return s[0]
		}
		idx := int(p * float64(len(s)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(s) {
			idx = len(s) - 1
		}
		return s[idx]
	}
	return pick(0.50), pick(0.95), pick(0.99), s[len(s)-1]
}

// LatencyReport is the flattened, serialisable percentile view in milliseconds.
type LatencyReport struct {
	Samples int     `json:"samples"`
	P50ms   float64 `json:"p50_ms"`
	P95ms   float64 `json:"p95_ms"`
	P99ms   float64 `json:"p99_ms"`
	Maxms   float64 `json:"max_ms"`
}

func (l *Latencies) Report() LatencyReport {
	p50, p95, p99, max := l.Percentiles()
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	return LatencyReport{
		Samples: l.Count(),
		P50ms:   ms(p50),
		P95ms:   ms(p95),
		P99ms:   ms(p99),
		Maxms:   ms(max),
	}
}
