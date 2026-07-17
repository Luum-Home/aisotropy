// eino_strengths_demo — the STEELMAN. What CloudWeGo Eino v0.9.12 is genuinely
// great at, proven by RUNNING it against the real framework (offline, no API key).
//
// The de-strawman established what Eino is NOT (a multi-tenant runtime / memory
// tier). This demo is the other half of a fair comparison: Eino is ByteDance's
// production graph-orchestration framework, and it is genuinely strong at
// TYPE-SAFE, COMPOSABLE, STREAMING orchestration in Go. aisotropy is a runtime,
// not a general graph-orchestration library — on this axis Eino is the reference,
// and it simply does not play in the tenancy/isolation space at all.
//
// Three strengths, each grounded in a real v0.9.12 symbol and executed:
//
//   1. Type-safe graph orchestration with Go generics + automatic mode boxing.
//      compose.NewGraph[I, O] / compose.InvokableLambda[I, O] give compile-time
//      checked node wiring; compose.Runnable[I, O] exposes Invoke/Stream/Collect/
//      Transform from ONE compiled graph — write an Invoke-only graph, call it as
//      a stream for free (the framework boxes it).
//
//   2. First-class stream-processing primitives (schema package).
//      schema.Pipe[T], StreamReader.Copy(n) (auto-managed fan-out, no manual
//      buffering) and MergeStreamReaders[T] (fan-in) are real, generic building
//      blocks — the plumbing most agent frameworks make you hand-roll.
//
//   3. Callbacks / aspects via handler injection.
//      callbacks.NewHandlerBuilder().OnStartFn/OnEndFn().Build() + compose.
//      WithCallbacks attach cross-cutting instrumentation around every component
//      WITHOUT touching node code — a clean AOP seam.
//
// Run:  go run ./cmd/strengths     (from arenas/eino/)
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func main() {
	ctx := context.Background()
	line := strings.Repeat("=", 70)
	fmt.Println(line)
	fmt.Println("Eino — STEELMAN: what the real v0.9.12 framework is genuinely great at")
	fmt.Println(line)
	fmt.Println("Eino is ByteDance's production graph-orchestration framework (LangGraph-for-Go).")
	fmt.Println("Its strength is TYPE-SAFE, COMPOSABLE, STREAMING orchestration — proven below by")
	fmt.Println("running the real API. (It has no tenancy/memory-isolation axis; that is a")
	fmt.Println("different category, covered honestly elsewhere in this arena.)")
	fmt.Println()

	pass := true
	pass = strength1TypeSafeGraph(ctx) && pass
	fmt.Println()
	pass = strength2StreamPrimitives() && pass
	fmt.Println()
	pass = strength3Callbacks(ctx) && pass

	fmt.Println()
	fmt.Println(line)
	if pass {
		fmt.Println("STRENGTHS RESULT: PASS — 3/3 Eino strengths demonstrated on the real v0.9.12 runtime.")
		fmt.Println("Verdict: on type-safe streaming graph orchestration, Eino is a genuinely strong,")
		fmt.Println("production-grade Go library. That is the axis it competes on — and it is a real one.")
		return
	}
	fmt.Println("STRENGTHS RESULT: FAIL — see markers above.")
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Strength 1: type-safe generic graph + automatic Invoke/Stream boxing.
// Real API: compose.NewGraph[I,O], compose.InvokableLambda[I,O], compose.Runnable[I,O].
// ---------------------------------------------------------------------------
func strength1TypeSafeGraph(ctx context.Context) bool {
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println("[1] Type-safe graph orchestration (Go generics) + free mode boxing")
	fmt.Println("    compose.NewGraph[string,int] · InvokableLambda[I,O] · Runnable[I,O]")
	fmt.Println(strings.Repeat("-", 70))
	pass := true

	// A graph typed string -> int. Node types must line up at COMPILE time:
	// START(string) -> parse(string->int) -> double(int->int) -> END(int).
	// A mismatched edge (e.g. wiring an int->int node to a string input) is a
	// Go compile error, not a runtime surprise — that is the whole point.
	g := compose.NewGraph[string, int]()

	if err := g.AddLambdaNode("parse", compose.InvokableLambda(
		func(_ context.Context, s string) (int, error) {
			return len(strings.Fields(s)), nil // word count
		})); err != nil {
		fmt.Println("  ✗ AddLambdaNode(parse):", err)
		return false
	}
	if err := g.AddLambdaNode("double", compose.InvokableLambda(
		func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		})); err != nil {
		fmt.Println("  ✗ AddLambdaNode(double):", err)
		return false
	}
	for _, e := range [][2]string{{compose.START, "parse"}, {"parse", "double"}, {"double", compose.END}} {
		if err := g.AddEdge(e[0], e[1]); err != nil {
			fmt.Printf("  ✗ AddEdge(%s->%s): %v\n", e[0], e[1], err)
			return false
		}
	}

	run, err := g.Compile(ctx) // -> compose.Runnable[string, int]
	if err != nil {
		fmt.Println("  ✗ Compile:", err)
		return false
	}

	const input = "the quick brown fox jumps" // 5 words -> *2 -> 10
	const want = 10

	// (a) Invoke: the graph was authored with Invoke-only lambdas.
	got, err := run.Invoke(ctx, input)
	if err != nil || got != want {
		fmt.Printf("  ✗ Invoke: got=%d want=%d err=%v\n", got, want, err)
		pass = false
	} else {
		fmt.Printf("  ✓ Invoke : %q -> %d (typed string->int graph, compile-checked wiring)\n", input, got)
	}

	// (b) Stream: the SAME compiled runnable serves a StreamReader[int] with no
	// extra code — Eino boxed the Invoke-only graph into all four Runnable modes.
	sr, err := run.Stream(ctx, input)
	if err != nil {
		fmt.Println("  ✗ Stream:", err)
		return false
	}
	defer sr.Close()
	var chunks []int
	for {
		v, rerr := sr.Recv()
		if rerr != nil {
			break // io.EOF
		}
		chunks = append(chunks, v)
	}
	sum := 0
	for _, c := range chunks {
		sum += c
	}
	if sum != want {
		fmt.Printf("  ✗ Stream: chunks=%v sum=%d want=%d\n", chunks, sum, want)
		pass = false
	} else {
		fmt.Printf("  ✓ Stream : same compiled graph served as StreamReader[int] -> chunks=%v (free boxing)\n", chunks)
	}
	fmt.Println("  Runnable[I,O] = {Invoke, Stream, Collect, Transform} from one authoring — a real,")
	fmt.Println("  generics-backed strength: author once, get every interaction mode, all type-checked.")
	return pass
}

// ---------------------------------------------------------------------------
// Strength 2: first-class stream primitives.
// Real API: schema.Pipe[T], StreamReader.Copy(n), schema.MergeStreamReaders[T].
// ---------------------------------------------------------------------------
func strength2StreamPrimitives() bool {
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println("[2] Stream-processing primitives (generic, in schema package)")
	fmt.Println("    schema.Pipe[T] · StreamReader.Copy(n) fan-out · MergeStreamReaders[T] fan-in")
	fmt.Println(strings.Repeat("-", 70))
	pass := true

	// Fan-out: one producer stream, Copy(3) yields 3 independent readers that EACH
	// see the full sequence — Eino manages the buffering, the caller does not.
	src := []int{1, 2, 3, 4, 5}
	sr, sw := schema.Pipe[int](0)
	go func() {
		for _, v := range src {
			sw.Send(v, nil)
		}
		sw.Close()
	}()

	readers := sr.Copy(3)
	want := []int{1, 2, 3, 4, 5}
	var wg sync.WaitGroup
	results := make([][]int, len(readers))
	for i, r := range readers {
		wg.Add(1)
		go func(i int, r *schema.StreamReader[int]) {
			defer wg.Done()
			defer r.Close()
			for {
				v, err := r.Recv()
				if err != nil {
					return
				}
				results[i] = append(results[i], v)
			}
		}(i, r)
	}
	wg.Wait()
	for i, got := range results {
		if fmt.Sprint(got) != fmt.Sprint(want) {
			fmt.Printf("  ✗ Copy reader %d: got=%v want=%v\n", i, got, want)
			pass = false
		}
	}
	if pass {
		fmt.Printf("  ✓ Copy(3): every one of 3 independent readers saw the full %v (auto-buffered fan-out)\n", want)
	}

	// Fan-in: merge N streams into one; every element arrives exactly once.
	a := schema.StreamReaderFromArray([]int{10, 20, 30})
	b := schema.StreamReaderFromArray([]int{40, 50})
	merged := schema.MergeStreamReaders([]*schema.StreamReader[int]{a, b})
	defer merged.Close()
	var all []int
	for {
		v, err := merged.Recv()
		if err != nil {
			break
		}
		all = append(all, v)
	}
	sort.Ints(all)
	wantMerged := []int{10, 20, 30, 40, 50}
	if fmt.Sprint(all) != fmt.Sprint(wantMerged) {
		fmt.Printf("  ✗ MergeStreamReaders: got=%v want=%v\n", all, wantMerged)
		pass = false
	} else {
		fmt.Printf("  ✓ MergeStreamReaders: 2 streams folded into 1 -> %v (exactly-once fan-in)\n", all)
	}
	fmt.Println("  Streaming is a native concern in Eino, not an afterthought: Pipe/Copy/Merge are")
	fmt.Println("  generic, composable primitives most agent frameworks force you to hand-roll.")
	return pass
}

// ---------------------------------------------------------------------------
// Strength 3: callbacks / aspects via handler injection.
// Real API: callbacks.NewHandlerBuilder().OnStartFn/OnEndFn().Build() + compose.WithCallbacks.
// ---------------------------------------------------------------------------
func strength3Callbacks(ctx context.Context) bool {
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println("[3] Callbacks / aspects: cross-cutting instrumentation without touching nodes")
	fmt.Println("    callbacks.NewHandlerBuilder().OnStartFn/OnEndFn().Build() + compose.WithCallbacks")
	fmt.Println(strings.Repeat("-", 70))
	pass := true

	// A tiny 2-node graph. The node bodies know NOTHING about instrumentation.
	g := compose.NewGraph[string, string]()
	_ = g.AddLambdaNode("upper", compose.InvokableLambda(
		func(_ context.Context, s string) (string, error) { return strings.ToUpper(s), nil }))
	_ = g.AddLambdaNode("bang", compose.InvokableLambda(
		func(_ context.Context, s string) (string, error) { return s + "!", nil }))
	_ = g.AddEdge(compose.START, "upper")
	_ = g.AddEdge("upper", "bang")
	_ = g.AddEdge("bang", compose.END)

	run, err := g.Compile(ctx)
	if err != nil {
		fmt.Println("  ✗ Compile:", err)
		return false
	}

	// The aspect: a real Eino handler that records a span per component start/end.
	var mu sync.Mutex
	var starts, ends int
	var seen []string
	h := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			mu.Lock()
			starts++
			if info != nil {
				// RunInfo carries Name (via WithNodeName), Type and Component;
				// build a best-effort label so the captured spans are legible.
				label := info.Name
				if label == "" {
					label = string(info.Component)
				}
				if label == "" {
					label = info.Type
				}
				seen = append(seen, label)
			}
			mu.Unlock()
			return ctx
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			mu.Lock()
			ends++
			mu.Unlock()
			return ctx
		}).
		Build()

	out, err := run.Invoke(ctx, "hello", compose.WithCallbacks(h))
	if err != nil {
		fmt.Println("  ✗ Invoke with callbacks:", err)
		return false
	}
	if out != "HELLO!" {
		fmt.Printf("  ✗ unexpected output: %q\n", out)
		pass = false
	}
	if starts == 0 || ends == 0 || starts != ends {
		fmt.Printf("  ✗ expected matched start/end spans, got starts=%d ends=%d\n", starts, ends)
		pass = false
	} else {
		fmt.Printf("  ✓ Output %q produced %d start/%d end spans around components, node code untouched\n", out, starts, ends)
		fmt.Printf("    Components observed: %v\n", seen)
	}
	fmt.Println("  One handler, attached at call time, wraps every component — the clean AOP seam")
	fmt.Println("  Eino's eino-ext observability backends (Langfuse/APMPlus/…) plug straight into.")
	return pass
}
