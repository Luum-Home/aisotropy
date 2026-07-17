package asyncbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaBroker adapts any Kafka-protocol broker via franz-go (client license:
// BSD-3-Clause — gate-passing). The SAME adapter drives two SERVERS with very
// different licenses, which is the whole point of the license column:
//
//   - Apache Kafka  (server: Apache-2.0)  -> gate-passing Kafka-API option
//   - Redpanda      (server: BSL)         -> MEASURED, but a license-gate BLOCKER
//     (source-available Business Source License; NOT adoptable without an ADR
//     exception per .claude/rules/cos/license-policy.md)
//
// Model: one topic per tenant `bench.evt.<tenant>` (single partition). A
// consumer subscribes to exactly one tenant topic, so tenant A can never see
// tenant B. At-least-once redelivery comes from consumer-group offset commits:
// uncommitted records are re-fetched by the next group member.
type KafkaBroker struct {
	seeds   []string
	name    string
	license string

	admin   *kadm.Client
	pcl     *kgo.Client // shared producer
	tenants []string

	mu     sync.Mutex
	closed bool
}

// NewKafkaBroker builds an adapter. name/license let one struct serve both the
// Apache-Kafka and the Redpanda runs with an honest license label.
func NewKafkaBroker(seeds []string, name, license string) (*KafkaBroker, error) {
	pcl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.ProducerBatchMaxBytes(1<<20),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(2*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	admin := kadm.NewClient(pcl)
	return &KafkaBroker{seeds: seeds, name: name, license: license, admin: admin, pcl: pcl}, nil
}

func (b *KafkaBroker) Name() string    { return b.name }
func (b *KafkaBroker) License() string { return b.license }

func (b *KafkaBroker) topic(tenant string) string {
	return "bench.evt." + sanitize(tenant)
}

func (b *KafkaBroker) Setup(ctx context.Context, tenants []string, group string) error {
	b.tenants = append([]string(nil), tenants...)
	topics := make([]string, 0, len(tenants))
	for _, t := range tenants {
		topics = append(topics, b.topic(t))
	}
	// RF=1, single partition: single-node bench cluster.
	resp, err := b.admin.CreateTopics(ctx, 1, 1, nil, topics...)
	if err != nil {
		return fmt.Errorf("kafka create topics: %w", err)
	}
	for _, ct := range resp {
		if ct.Err != nil && !isTopicExists(ct.Err) {
			return fmt.Errorf("kafka create topic %s: %w", ct.Topic, ct.Err)
		}
	}
	return nil
}

func (b *KafkaBroker) Publish(ctx context.Context, e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rec := &kgo.Record{
		Topic: b.topic(e.Tenant),
		Key:   []byte(e.Tenant),
		Value: data,
	}
	return b.pcl.ProduceSync(ctx, rec).FirstErr()
}

type kafkaConsumer struct {
	cl     *kgo.Client
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *kafkaConsumer) Close() error {
	c.cancel()
	<-c.done
	c.cl.Close()
	return nil
}

func (b *KafkaBroker) StartConsumer(ctx context.Context, tenant, group string, onMsg func(Envelope) bool) (io.Closer, error) {
	grp := sanitize(group) + "." + sanitize(tenant)
	cctx, cancel := context.WithCancel(ctx)
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(b.seeds...),
		kgo.ConsumerGroup(grp),
		kgo.ConsumeTopics(b.topic(tenant)), // hard tenant filter
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(), // ack == explicit commit of acked record
		// Short fetch wait: the broker must not hold a fetch when data is
		// available. The Apache-Kafka default (500ms) otherwise dominates the
		// measured latency and makes it a client artifact rather than a broker
		// property (Redpanda returns immediately, exposing the asymmetry).
		kgo.FetchMaxWait(20*time.Millisecond),
		kgo.SessionTimeout(6*time.Second),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kafka consumer client: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if cctx.Err() != nil {
				return
			}
			fs := cl.PollFetches(cctx)
			if fs.IsClientClosed() {
				return
			}
			var commit []*kgo.Record
			fs.EachRecord(func(r *kgo.Record) {
				var e Envelope
				if err := json.Unmarshal(r.Value, &e); err != nil {
					return
				}
				if onMsg(e) {
					commit = append(commit, r) // ack: eligible to commit
				}
				// false -> not committed -> redelivered by next group member.
			})
			if len(commit) > 0 {
				_ = cl.CommitRecords(context.Background(), commit...)
			}
		}
	}()
	return &kafkaConsumer{cl: cl, cancel: cancel, done: done}, nil
}

func (b *KafkaBroker) Teardown(ctx context.Context) error {
	if len(b.tenants) == 0 {
		return nil
	}
	topics := make([]string, 0, len(b.tenants))
	for _, t := range b.tenants {
		topics = append(topics, b.topic(t))
	}
	_, err := b.admin.DeleteTopics(ctx, topics...)
	return err
}

func (b *KafkaBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.pcl.Close()
	return nil
}

func isTopicExists(err error) bool {
	return err != nil && (contains(err.Error(), "already exists") || contains(err.Error(), "TOPIC_ALREADY_EXISTS"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
