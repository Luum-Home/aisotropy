package asyncbench

import (
	"context"
	"io"
)

// Broker is the minimal contract every measured message broker adapter
// implements. The benchmark runner drives all brokers through this one seam so
// the latency / throughput / redelivery / tenant-isolation harnesses are
// broker-agnostic.
type Broker interface {
	// Name is a short human label, e.g. "NATS JetStream".
	Name() string
	// License is the SERVER license of the broker (not the Go client). This is
	// the license-gate-relevant fact: the client libs are all MIT/Apache/BSD,
	// but the broker binary is what an operator would self-host.
	License() string

	// Setup provisions the per-tenant delivery primitive (JetStream stream +
	// per-tenant filtered consumers / Kafka topics / Valkey streams+groups) for
	// every tenant, and the named consumer group.
	Setup(ctx context.Context, tenants []string, group string) error

	// Publish sends one envelope on its tenant-scoped subject/topic/stream.
	// Broker-native dedup (by Envelope.ID) is enabled where the broker supports
	// it, mirroring the outbox's own idempotent-by-id property.
	Publish(ctx context.Context, e Envelope) error

	// StartConsumer runs a consumer bound to exactly ONE tenant. onMsg is called
	// for every envelope; returning true ACKs it, false leaves it unacked so the
	// broker redelivers (the crash-simulation lever). The returned Closer stops
	// the consumer WITHOUT acking anything in flight — a hard-kill analogue.
	StartConsumer(ctx context.Context, tenant, group string, onMsg func(Envelope) bool) (io.Closer, error)

	// Teardown removes the streams/topics/groups this run created. It must never
	// touch anything it did not create.
	Teardown(ctx context.Context) error

	// Close releases client connections.
	Close() error
}
