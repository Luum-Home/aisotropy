package asyncbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSBroker adapts NATS JetStream. Server license: Apache-2.0. Client
// (nats.go): Apache-2.0. This is the recommended gate-passing default.
//
// Model: one stream "BENCH" captures subject tree `bench.evt.>`; each tenant
// publishes to `bench.evt.<tenant>`. A per-(group,tenant) DURABLE pull consumer
// filters on exactly one tenant subject, so a consumer bound to tenant A can
// never observe tenant B. Broker-native dedup is on (Nats-Msg-Id = Envelope.ID)
// within a duplicate window, mirroring the outbox's idempotent-by-id property.
type NATSBroker struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	stream string

	mu     sync.Mutex
	closed bool
}

const natsStreamName = "BENCH"
const natsSubjectRoot = "bench.evt"
const natsAckWait = 4 * time.Second

func NewNATSBroker(url string) (*NATSBroker, error) {
	nc, err := nats.Connect(url,
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(10),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats jetstream: %w", err)
	}
	return &NATSBroker{nc: nc, js: js, stream: natsStreamName}, nil
}

func (b *NATSBroker) Name() string    { return "NATS JetStream" }
func (b *NATSBroker) License() string { return "Apache-2.0" }

func (b *NATSBroker) Setup(ctx context.Context, tenants []string, group string) error {
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       b.stream,
		Subjects:   []string{natsSubjectRoot + ".>"},
		Retention:  jetstream.LimitsPolicy,
		Storage:    jetstream.FileStorage,
		Duplicates: 5 * time.Minute, // Nats-Msg-Id dedup window
	})
	if err != nil {
		return fmt.Errorf("nats create stream: %w", err)
	}
	return nil
}

func (b *NATSBroker) subject(tenant string) string {
	return fmt.Sprintf("%s.%s", natsSubjectRoot, tenant)
}

func (b *NATSBroker) Publish(ctx context.Context, e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, b.subject(e.Tenant), data, jetstream.WithMsgID(e.ID))
	return err
}

type natsConsumer struct {
	cc jetstream.ConsumeContext
}

func (c *natsConsumer) Close() error {
	c.cc.Stop()
	return nil
}

func (b *NATSBroker) StartConsumer(ctx context.Context, tenant, group string, onMsg func(Envelope) bool) (io.Closer, error) {
	durable := fmt.Sprintf("%s_%s", sanitize(group), sanitize(tenant))
	cons, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: b.subject(tenant), // hard tenant filter
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       natsAckWait,
		MaxDeliver:    -1,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("nats consumer %s: %w", durable, err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var e Envelope
		if err := json.Unmarshal(msg.Data(), &e); err != nil {
			_ = msg.Term() // poison message: drop
			return
		}
		if onMsg(e) {
			_ = msg.Ack()
		}
		// On false: do NOTHING (no ack, no nak). This is the honest crash
		// analogue — a dead process cannot signal. AckWait expiry redelivers.
	})
	if err != nil {
		return nil, fmt.Errorf("nats consume: %w", err)
	}
	return &natsConsumer{cc: cc}, nil
}

func (b *NATSBroker) Teardown(ctx context.Context) error {
	err := b.js.DeleteStream(ctx, b.stream)
	if err != nil && err != jetstream.ErrStreamNotFound {
		return err
	}
	return nil
}

func (b *NATSBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.nc.Close()
	return nil
}
