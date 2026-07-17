// Command bench spins up each broker in Docker, runs the outbox->broker->consumer
// async benchmark against it, tears the container down, and writes results.txt.
//
// Every container it creates is named `asyncbench-*` and removed with `docker rm
// -f -v` in a deferred cleanup, so it never touches unrelated containers
// (aisotropy-rqlite-1, aisotropy-qdrant-1, woocommerce, github runners, ...).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	asyncbench "github.com/aisotropy/bench-async"
)

type brokerSpec struct {
	key       string
	container string
	image     string
	runArgs   []string // args after `docker run -d --name <c> ...`
	connect   func() (asyncbench.Broker, error)
	skipNote  string // if the image/run is unsupported, why
}

func main() {
	var (
		only   = flag.String("only", "", "comma-separated broker keys to run (nats,kafka,redpanda,valkey); empty = all")
		nLat   = flag.Int("n", 3000, "messages for latency/throughput")
		nRedel = flag.Int("redeliver", 200, "messages for the failure/redelivery test")
		nIso   = flag.Int("iso", 400, "messages per tenant for the isolation test")
		out    = flag.String("out", "results.txt", "results output path")
		keep   = flag.Bool("keep", false, "keep containers running after the run (debug)")
	)
	flag.Parse()

	specs := allSpecs()
	if *only != "" {
		want := map[string]bool{}
		for _, k := range strings.Split(*only, ",") {
			want[strings.TrimSpace(k)] = true
		}
		var filtered []brokerSpec
		for _, s := range specs {
			if want[s.key] {
				filtered = append(filtered, s)
			}
		}
		specs = filtered
	}

	// Tenants are disjoint per scenario so no broker state bleeds between tests.
	const group = "bench"
	latTenants := []string{"lat-a", "lat-b"}
	redelTenant := "rd-a"
	isoA, isoB := "iso-a", "iso-b"
	allTenants := append(append([]string{}, latTenants...), redelTenant, isoA, isoB)

	var results []asyncbench.Result
	for _, s := range specs {
		fmt.Printf("\n=== %s (%s) ===\n", s.key, s.image)
		if s.skipNote != "" {
			fmt.Printf("SKIP: %s\n", s.skipNote)
			results = append(results, asyncbench.Result{Broker: s.key, Err: fmt.Errorf("skipped: %s", s.skipNote)})
			continue
		}
		res := runBroker(s, group, allTenants, latTenants, redelTenant, isoA, isoB, *nLat, *nRedel, *nIso, *keep)
		results = append(results, res)
	}

	report := renderReport(results, *nLat, *nRedel, *nIso)
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write results: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nWrote %s\n", *out)
	fmt.Println(report)
}

func runBroker(s brokerSpec, group string, allTenants, latTenants []string, redelTenant, isoA, isoB string, nLat, nRedel, nIso int, keep bool) asyncbench.Result {
	res := asyncbench.Result{Broker: s.key}

	// Fresh container.
	_ = dockerRun("rm", "-f", "-v", s.container)
	runArgs := append([]string{"run", "-d", "--name", s.container}, s.runArgs...)
	if err := dockerRun(runArgs...); err != nil {
		res.Err = fmt.Errorf("docker run: %w", err)
		return res
	}
	if !keep {
		defer func() { _ = dockerRun("rm", "-f", "-v", s.container) }()
	}

	// Wait until the broker accepts a Setup (up to 90s).
	var b asyncbench.Broker
	deadline := time.Now().Add(90 * time.Second)
	for {
		var err error
		b, err = s.connect()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = b.Setup(ctx, allTenants, group)
			cancel()
			if err == nil {
				break
			}
			_ = b.Close()
		}
		if time.Now().After(deadline) {
			res.Err = fmt.Errorf("broker never became ready: %v", err)
			return res
		}
		time.Sleep(1500 * time.Millisecond)
	}
	defer b.Close()
	res.Broker = b.Name()
	res.License = b.License()
	fmt.Printf("ready: %s [%s]\n", b.Name(), b.License())

	ctx := context.Background()

	// 1) Latency + throughput.
	lat, pub, con, tput, err := asyncbench.RunLatencyThroughput(ctx, b, latTenants, group, nLat, 8)
	if err != nil {
		res.Err = fmt.Errorf("latency/throughput: %w", err)
	}
	res.Latency, res.Published, res.Consumed, res.ThroughputMsgPerSec = lat, pub, con, tput
	fmt.Printf("latency p50=%.2fms p95=%.2fms p99=%.2fms | throughput=%.0f msg/s (pub=%d con=%d)\n",
		lat.P50ms, lat.P95ms, lat.P99ms, tput, pub, con)

	// 2) Delivery under failure.
	rd, err := asyncbench.RunRedelivery(ctx, b, redelTenant, group, nRedel)
	if err != nil {
		fmt.Printf("redelivery ERROR: %v\n", err)
	}
	res.Redelivery = rd
	fmt.Printf("redelivery: published=%d unique=%d deliveries=%d dup=%d all-delivered=%v no-dup-facts=%v redelivery-proven=%v\n",
		rd.Published, rd.UniqueDelivered, rd.TotalDeliveries, rd.Duplicates, rd.AllDelivered, rd.NoDuplicateFacts, rd.RedeliveryProven)

	// 3) Tenant isolation.
	iso, err := asyncbench.RunIsolation(ctx, b, isoA, isoB, group, nIso)
	if err != nil {
		fmt.Printf("isolation ERROR: %v\n", err)
	}
	res.Isolation = iso
	fmt.Printf("isolation: A-received=%d B-leaked-to-A=%d isolated=%v\n", iso.AReceived, iso.BLeakedToA, iso.Isolated)

	// Clean the streams/topics we created.
	tctx, tcancel := context.WithTimeout(ctx, 15*time.Second)
	_ = b.Teardown(tctx)
	tcancel()
	return res
}

func allSpecs() []brokerSpec {
	return []brokerSpec{
		{
			key:       "nats",
			container: "asyncbench-nats",
			image:     "nats:2.10",
			runArgs:   []string{"-p", "14222:4222", "nats:2.10", "-js"},
			connect:   func() (asyncbench.Broker, error) { return asyncbench.NewNATSBroker("nats://127.0.0.1:14222") },
		},
		{
			key:       "kafka",
			container: "asyncbench-kafka",
			image:     "apache/kafka:3.9.0",
			runArgs: []string{
				"-p", "19092:9092",
				"-e", "KAFKA_NODE_ID=1",
				"-e", "KAFKA_PROCESS_ROLES=broker,controller",
				"-e", "KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093",
				"-e", "KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:19092",
				"-e", "KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
				"-e", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
				"-e", "KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
				"-e", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
				"-e", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1",
				"-e", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1",
				"-e", "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0",
				"apache/kafka:3.9.0",
			},
			connect: func() (asyncbench.Broker, error) {
				return asyncbench.NewKafkaBroker([]string{"127.0.0.1:19092"}, "Apache Kafka", "Apache-2.0")
			},
		},
		{
			key:       "redpanda",
			container: "asyncbench-redpanda",
			image:     "redpandadata/redpanda:v24.2.7",
			runArgs: []string{
				"-p", "19093:19093",
				"redpandadata/redpanda:v24.2.7",
				"redpanda", "start",
				"--overprovisioned", "--smp", "1", "--memory", "1G", "--reserve-memory", "0M",
				"--node-id", "0", "--check=false",
				"--kafka-addr", "PLAINTEXT://0.0.0.0:19093",
				"--advertise-kafka-addr", "PLAINTEXT://localhost:19093",
			},
			connect: func() (asyncbench.Broker, error) {
				// Server license: BSL (Business Source License) — flagged BLOCKER.
				return asyncbench.NewKafkaBroker([]string{"127.0.0.1:19093"}, "Redpanda", "BSL (source-available) [LICENSE-GATE BLOCKER]")
			},
		},
		{
			key:       "valkey",
			container: "asyncbench-valkey",
			image:     "valkey/valkey:8",
			runArgs:   []string{"-p", "16379:6379", "valkey/valkey:8"},
			connect:   func() (asyncbench.Broker, error) { return asyncbench.NewValkeyBroker("127.0.0.1:16379") },
		},
	}
}

func dockerRun(args ...string) error {
	cmd := exec.Command("docker", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

func renderReport(results []asyncbench.Result, nLat, nRedel, nIso int) string {
	var sb strings.Builder
	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(&sb, "aisotropy — async broker benchmark (outbox -> broker -> consumer)\n")
	fmt.Fprintf(&sb, "generated: %s\n", ts)
	fmt.Fprintf(&sb, "workload: latency/throughput=%d msgs (8 confirmed-publish workers), redelivery=%d msgs, isolation=%d msgs/tenant\n\n", nLat, nRedel, nIso)

	fmt.Fprintf(&sb, "%-16s %-38s %10s %10s %10s %12s\n", "BROKER", "SERVER LICENSE", "p50(ms)", "p95(ms)", "p99(ms)", "msg/s")
	fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 100))
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(&sb, "%-16s %-38s %10s %10s %10s %12s   (%v)\n", r.Broker, r.License, "-", "-", "-", "-", r.Err)
			continue
		}
		fmt.Fprintf(&sb, "%-16s %-38s %10.2f %10.2f %10.2f %12.0f\n",
			r.Broker, r.License, r.Latency.P50ms, r.Latency.P95ms, r.Latency.P99ms, r.ThroughputMsgPerSec)
	}

	fmt.Fprintf(&sb, "\nDELIVERY UNDER FAILURE (kill consumer mid-batch -> assert at-least-once, no duplicate facts):\n")
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		rd := r.Redelivery
		verdict := "PASS"
		if !rd.AllDelivered || !rd.NoDuplicateFacts {
			verdict = "FAIL"
		}
		fmt.Fprintf(&sb, "  %-16s published=%d unique-facts=%d deliveries=%d duplicates=%d | all-delivered=%v no-dup-facts=%v redelivery-observed=%v => %s\n",
			r.Broker, rd.Published, rd.UniqueDelivered, rd.TotalDeliveries, rd.Duplicates, rd.AllDelivered, rd.NoDuplicateFacts, rd.RedeliveryProven, verdict)
		if rd.Note != "" {
			fmt.Fprintf(&sb, "       note: %s\n", rd.Note)
		}
	}

	fmt.Fprintf(&sb, "\nTENANT ISOLATION (consumer bound to tenant A must never receive tenant B):\n")
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		iso := r.Isolation
		verdict := "PASS"
		if !iso.Isolated {
			verdict = "FAIL"
		}
		fmt.Fprintf(&sb, "  %-16s A-received=%d B-leaked-to-A=%d => %s\n", r.Broker, iso.AReceived, iso.BLeakedToA, verdict)
	}

	// Machine-readable appendix.
	fmt.Fprintf(&sb, "\n--- JSON ---\n")
	enc, _ := json.MarshalIndent(results, "", "  ")
	sb.Write(enc)
	sb.WriteByte('\n')
	return sb.String()
}
