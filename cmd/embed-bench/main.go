// Command embed-bench is the Bet-5 (ADR-0023) latency benchmark that decides
// the next in-process brick: it compares the CURRENT path — a fresh candle
// subprocess per embedding (exactly what internal/semantic.LocalEmbedder does:
// spawn + model load + infer, --input/--output files) — against a WARM candle
// daemon (the same binary run with --serve, model loaded ONCE, requests piped
// over stdin/stdout). Same crate, same weights, apples-to-apples.
//
// The question it answers: is the subprocess millisecond mostly PROCESS SPAWN +
// MODEL LOAD (which a warm daemon removes) or the model's own INFERENCE FLOOR
// (which no binding — cgo, daemon, ONNX — can fix)? If warm per-call << cold
// per-call, a daemon/cgo path is worth building; if warm ≈ cold minus a trivial
// delta, the model is the floor and the answer is a smaller/faster model.
//
// It fabricates NO numbers: if the scorer binary or model dir is missing it
// SKIPS cleanly (exit 0), like cmd/bitrate-harness.
//
// RUN:
//	AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \
//	AISOTROPY_EMBED_MODEL_DIR=/abs/model-dir \
//	  go run ./cmd/embed-bench -n 30 -m 200
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type request struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// A short, mixed ES/EN probe — representative of a tenant turn, not a torture test.
const probe = "el gato duerme en la ventana mientras el sol calienta la habitación; the cat is asleep"

func main() {
	os.Exit(run())
}

func run() int {
	n := flag.Int("n", 30, "cold-subprocess iterations (fresh spawn + load + infer each)")
	m := flag.Int("m", 200, "warm-daemon iterations (after the one-time cold start)")
	flag.Parse()

	bin := os.Getenv("AISOTROPY_EMBED_BIN")
	if bin == "" {
		fmt.Println("SKIP: AISOTROPY_EMBED_BIN unset — point it at the built embed binary (crates/embed/target/release/embed).")
		return 0
	}
	if _, err := os.Stat(bin); err != nil {
		fmt.Printf("SKIP: embed binary %q not found: %v\n", bin, err)
		return 0
	}
	modelDir := os.Getenv("AISOTROPY_EMBED_MODEL_DIR")
	if modelDir == "" {
		fmt.Println("SKIP: AISOTROPY_EMBED_MODEL_DIR unset — point it at a dir with config.json + model.safetensors + tokenizer.json.")
		return 0
	}
	for _, f := range []string{"config.json", "model.safetensors", "tokenizer.json"} {
		if _, err := os.Stat(filepath.Join(modelDir, f)); err != nil {
			fmt.Printf("SKIP: model dir %q missing %s\n", modelDir, f)
			return 0
		}
	}

	fmt.Println("== Bet-5 embed latency benchmark (ADR-0023 in-process decision) ==")
	fmt.Printf("binary:   %s\n", bin)
	fmt.Printf("model:    %s\n", modelDir)
	fmt.Printf("iters:    cold=%d  warm=%d\n", *n, *m)
	fmt.Println("cold = fresh subprocess per call (spawn+load+infer, today's LocalEmbedder path)")
	fmt.Println("warm = long-lived --serve daemon, model loaded once, one call over the pipe")
	fmt.Println()

	cold, err := measureCold(bin, modelDir, *n)
	if err != nil {
		fmt.Printf("ERROR cold: %v\n", err)
		return 1
	}
	warmColdStart, warm, err := measureWarm(bin, modelDir, *m)
	if err != nil {
		fmt.Printf("ERROR warm: %v\n", err)
		return 1
	}

	cs := stat(cold)
	ws := stat(warm)
	fmt.Println("-- results (per single 384-d embedding) --")
	fmt.Printf("COLD subprocess:  p50=%-8s p95=%-8s mean=%-8s min=%-8s (n=%d)\n",
		ms(cs.p50), ms(cs.p95), ms(cs.mean), ms(cs.min), len(cold))
	fmt.Printf("WARM daemon:      p50=%-8s p95=%-8s mean=%-8s min=%-8s (n=%d)\n",
		ms(ws.p50), ms(ws.p95), ms(ws.mean), ms(ws.min), len(warm))
	fmt.Printf("daemon cold-start (one-time, first call incl. model load): %s\n", ms(warmColdStart))
	fmt.Println()

	// The load-bearing ratio: how much of the cold subprocess ms is spawn+load
	// tax that the warm path removes.
	tax := cs.p50 - ws.p50
	speedup := float64(cs.p50) / float64(ws.p50)
	fmt.Println("-- verdict (numbers) --")
	fmt.Printf("spawn+load tax removed by staying warm (cold p50 - warm p50) = %s\n", ms(tax))
	fmt.Printf("warm speedup over cold (cold p50 / warm p50)                 = %.1fx\n", speedup)
	switch {
	case speedup >= 3:
		fmt.Println("=> The subprocess ms is DOMINATED by spawn+model-load. A warm daemon (or")
		fmt.Println("   cgo, same warm process) is worth building — inference floor is well below")
		fmt.Println("   the current per-call cost. candle-cgo would land near the warm number")
		fmt.Println("   (it only removes the local-pipe IPC on top of the daemon).")
	case speedup >= 1.3:
		fmt.Println("=> Staying warm helps moderately; part of the ms is spawn+load, part is the")
		fmt.Println("   model. Worth a daemon; cgo's extra gain over the daemon is likely small.")
	default:
		fmt.Println("=> Little to gain from staying warm — the cost IS the model's inference")
		fmt.Println("   floor, not spawn/load. No binding (daemon/cgo/ONNX) fixes this; the")
		fmt.Println("   lever is a smaller/faster model or quantization, not the transport.")
	}
	fmt.Println()
	fmt.Println("NOTE: single-machine, CPU, one model. Absolute numbers are machine-specific;")
	fmt.Println("      the COLD-vs-WARM ratio is the transferable finding.")
	return 0
}

// measureCold times the exact path LocalEmbedder uses today: write a one-line
// NDJSON request to a temp file, exec `bin --input in --output out`, wait, read
// the result. Each iteration pays a full process spawn + model load + inference.
func measureCold(bin, modelDir string, n int) ([]time.Duration, error) {
	dir, err := os.MkdirTemp("", "embed-bench-cold")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	line, _ := json.Marshal(request{ID: "p", Text: probe})

	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		in := filepath.Join(dir, "in.ndjson")
		res := filepath.Join(dir, "out.ndjson")
		if err := os.WriteFile(in, append(line, '\n'), 0o600); err != nil {
			return nil, err
		}
		cmd := exec.Command(bin, "--input", in, "--output", res)
		cmd.Env = append(os.Environ(), "AISOTROPY_EMBED_MODEL_DIR="+modelDir)
		start := time.Now()
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("iter %d: %w", i, err)
		}
		out = append(out, time.Since(start))
		if _, err := os.Stat(res); err != nil {
			return nil, fmt.Errorf("iter %d: no output: %w", i, err)
		}
	}
	return out, nil
}

// measureWarm spawns the binary once with --serve, then pipes m requests over
// stdin and reads one response line each. The first call includes the one-time
// model load (returned separately); the rest are warm per-call latencies.
func measureWarm(bin, modelDir string, m int) (time.Duration, []time.Duration, error) {
	cmd := exec.Command(bin, "--serve")
	cmd.Env = append(os.Environ(), "AISOTROPY_EMBED_MODEL_DIR="+modelDir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, nil, err
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20) // 384 floats fit; be generous.
	line, _ := json.Marshal(request{ID: "p", Text: probe})
	line = append(line, '\n')

	roundtrip := func() (time.Duration, error) {
		start := time.Now()
		if _, err := stdin.Write(line); err != nil {
			return 0, err
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("daemon closed stdout")
		}
		return time.Since(start), nil
	}

	// First call = cold start (spawn already happened; this pays model load).
	coldStart, err := roundtrip()
	if err != nil {
		return 0, nil, fmt.Errorf("warm cold-start: %w", err)
	}

	out := make([]time.Duration, 0, m)
	for i := 0; i < m; i++ {
		d, err := roundtrip()
		if err != nil {
			return 0, nil, fmt.Errorf("warm iter %d: %w", i, err)
		}
		out = append(out, d)
	}
	return coldStart, out, nil
}

type stats struct{ p50, p95, mean, min time.Duration }

func stat(ds []time.Duration) stats {
	if len(ds) == 0 {
		return stats{}
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return stats{
		p50:  s[len(s)*50/100],
		p95:  s[min(len(s)*95/100, len(s)-1)],
		mean: sum / time.Duration(len(s)),
		min:  s[0],
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.3fms", float64(d.Microseconds())/1000.0) }
