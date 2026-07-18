package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// benchCell is one (framework, form, mode) benchmark result. Form-level
// measurements (cold-start, ttft, image footprint, build time, peak RSS) are the
// same for every mode of a given framework×form and are duplicated onto each
// cell; the results writer deduplicates them into their own tables.
type benchCell struct {
	Framework string
	Form      string // native | docker
	Mode      string

	Iters   int
	WarmP50 int64 // steady-state wall clock, ms
	WarmP95 int64
	InTok   int // provider-reported, representative warm run
	OutTok  int

	// form-level (repeated across a framework×form's mode cells)
	ColdMs    int64  // spawn-to-first-output, empty stdin, NO model call
	TTFTMs    int64  // from a stream-mode run
	PeakRSSKB int64  // getrusage Maxrss of the native child (native form only)
	ImageSize string // docker image size (docker form only)
	BuildMs   int64  // docker build wall clock when built during this run (0 = not built now)

	Skipped bool
	Note    string
}

// runBenchmarks fills the benchmark grid: framework × form × bench-mode. It is
// automatic — no manual step. Latency is measured on the (cheap) bench modes to
// bound spend; cold-start uses an empty-stdin probe that costs zero model calls;
// footprint/build-time need no model calls either.
func runBenchmarks(frameworks []adapter, benchModes, forms []string, tasks []task, rag []ragQuestion, iters, coldProbes int, budget *callCounter) []benchCell {
	dockerOK := dockerAvailable()
	var cells []benchCell

	for _, fw := range frameworks {
		for _, form := range forms {
			script := fw.script(form)
			if form == "docker" && script == "" {
				cells = append(cells, benchCell{Framework: fw.name, Form: form, Mode: "*", Skipped: true,
					Note: "native-only adapter (no run-docker.sh)"})
				continue
			}
			if form == "docker" && !dockerOK {
				cells = append(cells, benchCell{Framework: fw.name, Form: form, Mode: "*", Skipped: true,
					Note: "docker unavailable on this host"})
				continue
			}

			// Form-level, model-call-free measurements first.
			var imageSize string
			var buildMs int64
			if form == "docker" {
				imageSize, buildMs = ensureDockerImage(fw)
			}
			coldMs := coldStart(script, coldProbes)
			logf("[bench] %s/%s cold-start=%dms (n=%d, no model call)", fw.name, form, coldMs, coldProbes)

			// TTFT from a single stream-mode run (one model call).
			ttft := int64(0)
			if in, ok := representativeInput("stream", tasks, rag); ok {
				res := runAdapter(script, in, 180*time.Second)
				budget.add(1)
				if res.runErr == "" && res.out.Error == "" {
					ttft = res.out.TTFTMs
				}
				logf("[bench] %s/%s stream ttft=%dms", fw.name, form, ttft)
			}

			for _, mode := range benchModes {
				in, ok := representativeInput(mode, tasks, rag)
				if !ok {
					continue
				}
				var walls []int64
				var peakRSS int64
				inTok, outTok := -1, -1
				fails := 0
				for i := 0; i < iters; i++ {
					res := runAdapter(script, in, 180*time.Second)
					budget.add(1)
					if res.runErr != "" || res.out.Error != "" {
						fails++
						continue
					}
					walls = append(walls, res.wallMs)
					if res.maxRSSKB > peakRSS {
						peakRSS = res.maxRSSKB
					}
					if inTok < 0 && res.out.InputTokens >= 0 {
						inTok, outTok = res.out.InputTokens, res.out.OutputTokens
					}
				}
				cell := benchCell{
					Framework: fw.name, Form: form, Mode: mode, Iters: len(walls),
					InTok: inTok, OutTok: outTok,
					ColdMs: coldMs, TTFTMs: ttft, PeakRSSKB: peakRSS,
					ImageSize: imageSize, BuildMs: buildMs,
				}
				if len(walls) == 0 {
					cell.Skipped = true
					cell.Note = fmt.Sprintf("all %d iterations failed (mode unsupported by adapter?)", iters)
				} else {
					cell.WarmP50 = percentile(walls, 50)
					cell.WarmP95 = percentile(walls, 95)
					if fails > 0 {
						cell.Note = fmt.Sprintf("%d/%d iterations failed", fails, iters)
					}
				}
				logf("[bench] %s/%s/%s p50=%dms p95=%dms (%d ok)", fw.name, form, mode, cell.WarmP50, cell.WarmP95, len(walls))
				cells = append(cells, cell)
			}
		}
	}
	return cells
}

// coldStart measures the median spawn-to-first-output wall clock over n probes,
// feeding EMPTY stdin. Every adapter reads stdin, fails to parse the empty body,
// emits its JSON error object, and exits — so this isolates process/container +
// runtime startup with NO model round-trip.
func coldStart(script string, n int) int64 {
	if n < 1 {
		n = 1
	}
	var walls []int64
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		cmd := exec.CommandContext(ctx, "bash", script)
		cmd.Stdin = bytes.NewReader(nil) // empty → immediate EOF → parse-fail, no model call
		var devnull bytes.Buffer
		cmd.Stdout = &devnull
		cmd.Stderr = &devnull
		cmd.Env = append(os.Environ(), frameworkCollectionEnv(script)...)
		start := time.Now()
		_ = cmd.Run() // non-zero exit is expected (parse failure)
		walls = append(walls, time.Since(start).Milliseconds())
		cancel()
	}
	return percentile(walls, 50)
}

// representativeInput returns one canonical request for a mode, used for latency
// benchmarking (kept small to bound spend).
func representativeInput(mode string, tasks []task, rag []ragQuestion) (adapterInput, bool) {
	switch mode {
	case "qa", "structured", "stream":
		for _, t := range tasks {
			if t.Kind == "single_shot_qa" {
				return adapterInput{Mode: mode, System: t.System, Messages: t.Messages}, true
			}
		}
	case "memory":
		for _, t := range tasks {
			if t.Kind == "multi_turn_memory" {
				// Single-invocation persist call: enough to time the mode's path.
				return adapterInput{Mode: "memory", System: t.System, Messages: t.Messages,
					SessionID: "arena-bench-" + t.ID}, true
			}
		}
	case "tools":
		a := arithmeticTasks[0]
		return adapterInput{Mode: "tools", System: toolSystem,
			Messages: []inMessage{{Role: "user", Content: a.question}}}, true
	case "rag":
		for _, q := range rag {
			if !q.Trap {
				return adapterInput{Mode: "rag", TopK: 4,
					Messages: []inMessage{{Role: "user", Content: q.Question}}}, true
			}
		}
	}
	return adapterInput{}, false
}

// ---- docker footprint -------------------------------------------------------

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "info").Run() == nil
}

var imageTagRe = regexp.MustCompile(`(?m)^IMAGE=["']?([^"'\s]+)`)

// dockerImageTag reads the IMAGE="..." line from an adapter's run-docker.sh.
func dockerImageTag(fw adapter) string {
	if fw.runDocker == "" {
		return ""
	}
	b, err := os.ReadFile(fw.runDocker)
	if err != nil {
		return ""
	}
	if m := imageTagRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return fw.name + "-live-arena:local"
}

// ensureDockerImage returns the image size (humanized) and, if the image had to
// be built during this run, the build wall clock in ms (0 otherwise).
func ensureDockerImage(fw adapter) (size string, buildMs int64) {
	tag := dockerImageTag(fw)
	if tag == "" {
		return "n/a", 0
	}
	if !dockerImageExists(tag) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 900*time.Second)
		cmd := exec.CommandContext(ctx, "docker", "build", "-t", tag, fw.dir)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // build log to our stderr
		err := cmd.Run()
		cancel()
		buildMs = time.Since(start).Milliseconds()
		if err != nil {
			return "build failed", buildMs
		}
		logf("[bench] built %s in %dms", tag, buildMs)
	}
	return dockerImageSizeHuman(tag), buildMs
}

func dockerImageExists(tag string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", tag).Run() == nil
}

func dockerImageSizeHuman(tag string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "-f", "{{.Size}}", tag).Output()
	if err != nil {
		return "n/a"
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return "n/a"
	}
	return humanizeBytes(n)
}

func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- percentile -------------------------------------------------------------

// percentile returns the p-th percentile (nearest-rank) of vals in ms.
func percentile(vals []int64, p int) int64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 1 {
		return s[0]
	}
	rank := (p * len(s)) / 100
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}
