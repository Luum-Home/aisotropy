package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

// serverMetrics is shared across every transport. The cancellation counters are
// the §7.3 correctness gate: when a client disconnects mid-stream, the server's
// request context is cancelled, the stub stops, and streamCancelled increments.
// If a transport failed to propagate cancellation, streamCancelled would stay 0
// while streamActive stayed pinned — a leak we can detect.
type serverMetrics struct {
	streamOpened    atomic.Int64 // total streams started
	streamActive    atomic.Int64 // currently open
	streamCompleted atomic.Int64 // ran to the last token
	streamCancelled atomic.Int64 // stopped early because client/ctx was cancelled
	maxActive       atomic.Int64 // high-water mark of concurrent streams
	tokensSent      atomic.Int64 // total tokens written across all streams
}

func (m *serverMetrics) open() {
	m.streamOpened.Add(1)
	n := m.streamActive.Add(1)
	for {
		hi := m.maxActive.Load()
		if n <= hi || m.maxActive.CompareAndSwap(hi, n) {
			break
		}
	}
}

func (m *serverMetrics) close(completed bool) {
	m.streamActive.Add(-1)
	if completed {
		m.streamCompleted.Add(1)
	} else {
		m.streamCancelled.Add(1)
	}
}

type statsSnapshot struct {
	Time            string `json:"time"`
	Goroutines      int    `json:"goroutines"`
	HeapAllocBytes  uint64 `json:"heap_alloc_bytes"`
	HeapAllocMB     int    `json:"heap_alloc_mb"`
	StreamOpened    int64  `json:"stream_opened"`
	StreamActive    int64  `json:"stream_active"`
	StreamCompleted int64  `json:"stream_completed"`
	StreamCancelled int64  `json:"stream_cancelled"`
	MaxActive       int64  `json:"max_active"`
	TokensSent      int64  `json:"tokens_sent"`
}

func (m *serverMetrics) snapshot() statsSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return statsSnapshot{
		Time:            time.Now().Format(time.RFC3339Nano),
		Goroutines:      runtime.NumGoroutine(),
		HeapAllocBytes:  ms.HeapAlloc,
		HeapAllocMB:     int(ms.HeapAlloc / (1024 * 1024)),
		StreamOpened:    m.streamOpened.Load(),
		StreamActive:    m.streamActive.Load(),
		StreamCompleted: m.streamCompleted.Load(),
		StreamCancelled: m.streamCancelled.Load(),
		MaxActive:       m.maxActive.Load(),
		TokensSent:      m.tokensSent.Load(),
	}
}

func (m *serverMetrics) statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.snapshot())
}

// selfSignedTLS builds an in-memory self-signed cert (localhost + 127.0.0.1)
// with ALPN advertising h2 then http/1.1, so the TLS listener negotiates
// HTTP/2 with compliant clients and falls back to HTTP/1.1 otherwise.
func selfSignedTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
