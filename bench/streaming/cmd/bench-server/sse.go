package main

import (
	"net/http"
	"strconv"
	"time"

	"aisotropy.local/bench/streaming/internal/tokens"
)

// sseHandler streams the deterministic token stub as Server-Sent Events. The
// same handler is mounted on both the plaintext (HTTP/1.1) and TLS (HTTP/2)
// listeners; r.ProtoMajor tells which transport carried the request, which is
// the whole point of the h1-vs-h2 comparison — identical application code, the
// ONLY variable is the wire protocol underneath.
func (s *server) sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stream := streamFromQuery(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Proto", r.Proto)
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // flush headers so the client sees the response open (TTFT gate)

	s.metrics.open()
	completed := false
	defer func() { s.metrics.close(completed) }()

	buf := make([]byte, 0, 64)
	err := stream.Emit(r.Context(), func(t tokens.Token) error {
		buf = buf[:0]
		buf = append(buf, "data: "...)
		buf = strconv.AppendInt(buf, int64(t.Index), 10)
		buf = append(buf, '|')
		buf = append(buf, t.Text...)
		buf = append(buf, '|')
		buf = strconv.AppendInt(buf, time.Now().UnixNano(), 10)
		buf = append(buf, '\n', '\n')
		if _, werr := w.Write(buf); werr != nil {
			return werr // client gone -> stops the stub (cancellation gate)
		}
		flusher.Flush()
		s.metrics.tokensSent.Add(1)
		return nil
	})
	if err == nil {
		completed = true
	}
}

// streamFromQuery lets a load client override the stub shape (?count=&interval_ms=)
// but defaults to the canonical stub so every transport is compared on equal terms.
func streamFromQuery(r *http.Request) tokens.Stream {
	s := tokens.DefaultStream()
	if v := r.URL.Query().Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.Count = n
		}
	}
	if v := r.URL.Query().Get("interval_ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.Interval = time.Duration(n) * time.Millisecond
		}
	}
	return s
}
