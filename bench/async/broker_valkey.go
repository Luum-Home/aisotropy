package asyncbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// ValkeyBroker adapts Valkey Streams. Server license: BSD-3-Clause (Valkey is
// the Linux-Foundation fork of Redis created precisely to stay open after Redis
// went source-available). Client (valkey-go): Apache-2.0. Both gate-passing.
//
// Model: one stream key per tenant `bench:stream:<tenant>` with a consumer
// group. A consumer reads ONLY its tenant's stream key, so tenant A can never
// see tenant B (isolation by key namespace). At-least-once redelivery comes from
// the group Pending Entries List (PEL): entries delivered by XREADGROUP but not
// XACKed stay pending; a restarted consumer with the SAME name re-reads them via
// XREADGROUP id "0" before consuming new (id ">").
type ValkeyBroker struct {
	client valkey.Client
	group  string

	mu      sync.Mutex
	tenants []string
	closed  bool
}

func NewValkeyBroker(addr string) (*ValkeyBroker, error) {
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:  []string{addr},
		DisableCache: true,
	})
	if err != nil {
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &ValkeyBroker{client: client}, nil
}

func (b *ValkeyBroker) Name() string    { return "Valkey Streams" }
func (b *ValkeyBroker) License() string { return "BSD-3-Clause" }

func (b *ValkeyBroker) streamKey(tenant string) string {
	return "bench:stream:" + sanitize(tenant)
}

func (b *ValkeyBroker) Setup(ctx context.Context, tenants []string, group string) error {
	b.group = sanitize(group)
	b.tenants = append([]string(nil), tenants...)
	for _, t := range tenants {
		key := b.streamKey(t)
		// XGROUP CREATE ... MKSTREAM: create stream + group at id 0.
		err := b.client.Do(ctx, b.client.B().XgroupCreate().Key(key).Group(b.group).Id("0").Mkstream().Build()).Error()
		if err != nil && !isBusyGroup(err) {
			return fmt.Errorf("valkey xgroup create %s: %w", key, err)
		}
	}
	return nil
}

func (b *ValkeyBroker) Publish(ctx context.Context, e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	key := b.streamKey(e.Tenant)
	// XADD key * d <json>. (Stream IDs are time-ordered; app-level dedup is the
	// oracle's job, matching the outbox's idempotent sink contract.)
	return b.client.Do(ctx, b.client.B().Xadd().Key(key).Id("*").FieldValue().FieldValue("d", string(data)).Build()).Error()
}

type valkeyConsumer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *valkeyConsumer) Close() error {
	c.cancel()
	<-c.done
	return nil
}

func (b *ValkeyBroker) StartConsumer(ctx context.Context, tenant, group string, onMsg func(Envelope) bool) (io.Closer, error) {
	key := b.streamKey(tenant)
	grp := sanitize(group)
	consumer := grp + "-" + sanitize(tenant) + "-c" // STABLE across restarts => inherits PEL
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		mode := "0" // drain this consumer's pending (redelivery) first, then ">"
		for {
			if cctx.Err() != nil {
				return
			}
			resp := b.client.Do(cctx, b.client.B().
				Xreadgroup().Group(grp, consumer).Count(128).Block(500).
				Streams().Key(key).Id(mode).Build())
			if err := resp.Error(); err != nil {
				if valkey.IsValkeyNil(err) { // no data this tick
					if mode == "0" {
						mode = ">"
					}
					continue
				}
				if cctx.Err() != nil {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			m, err := resp.AsXRead()
			if err != nil {
				if mode == "0" {
					mode = ">"
				}
				continue
			}
			entries := m[key]
			if len(entries) == 0 {
				if mode == "0" {
					mode = ">"
				}
				continue
			}
			var ackIDs []string
			for _, en := range entries {
				var e Envelope
				if err := json.Unmarshal([]byte(en.FieldValues["d"]), &e); err != nil {
					continue
				}
				if onMsg(e) {
					ackIDs = append(ackIDs, en.ID) // ack after processing
				}
				// false -> leave in PEL -> redelivered to same-named restart.
			}
			// Batch XACK the whole read in ONE round-trip: at-least-once is
			// preserved (ack happens only after onMsg), but per-message RTT no
			// longer serialises the consumer (the prior per-msg XACK dominated
			// the measured latency).
			if len(ackIDs) > 0 {
				_ = b.client.Do(cctx, b.client.B().Xack().Key(key).Group(grp).Id(ackIDs...).Build()).Error()
			}
			if mode == "0" && len(entries) < 128 {
				mode = ">" // pending exhausted
			}
		}
	}()
	return &valkeyConsumer{cancel: cancel, done: done}, nil
}

func (b *ValkeyBroker) Teardown(ctx context.Context) error {
	b.mu.Lock()
	tenants := append([]string(nil), b.tenants...)
	b.mu.Unlock()
	for _, t := range tenants {
		_ = b.client.Do(ctx, b.client.B().Del().Key(b.streamKey(t)).Build()).Error()
	}
	return nil
}

func (b *ValkeyBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.client.Close()
	return nil
}

func isBusyGroup(err error) bool {
	return err != nil && contains(err.Error(), "BUSYGROUP")
}
