package asyncbench

import "time"

// Envelope mirrors, byte-for-byte, the JSON contract of
// internal/memory.OutboxEvent (see internal/memory/outbox.go, the source of
// truth). It is duplicated here rather than imported because bench/async is a
// separate Go module: Go forbids importing an `internal/` package across a
// module boundary. The json tags are kept identical so a real drained
// OutboxEvent serialises into exactly this shape and vice-versa.
//
// Real contract (outbox.go):
//
//	type OutboxEvent struct {
//	    ID        string    `json:"id"`
//	    Tenant    string    `json:"tenant"`
//	    User      string    `json:"user"`
//	    Session   string    `json:"session"`
//	    Content   string    `json:"content"`
//	    CreatedAt time.Time `json:"created_at"`
//	}
//
// PubNanos is an ADDITIVE benchmark-only field: the monotonic-clock publish
// timestamp used to measure enqueue -> broker -> consumer-ACK latency without a
// distributed clock (publisher and consumer share one process). It is namespaced
// under `_bench_pub_nanos` so it never collides with a real outbox field and is
// ignored by any real consumer.
type Envelope struct {
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant"`
	User      string    `json:"user"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`

	PubNanos int64 `json:"_bench_pub_nanos,omitempty"`
}
