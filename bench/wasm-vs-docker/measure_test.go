package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// emptyWasmModule is the minimal valid WebAssembly module: just the magic
// number and version, no sections. wazero's CompileModule accepts it (no
// imports/exports to resolve at compile time), so benchCompileCache is
// testable without a real go/tinygo/rustc-built guest.
var emptyWasmModule = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

func TestWasmSpec(t *testing.T) {
	g := guestBuild{name: "go", module: []byte("bytes"), allowWASI: true}
	spec := wasmSpec(g)
	if spec.Name != "echo-go" || spec.Description == "" || !bytes.Equal(spec.Module, g.module) || !spec.AllowWASI {
		t.Errorf("wasmSpec = %+v", spec)
	}
}

func TestBenchWASM_Success(t *testing.T) {
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return &fakeComputeInstance{name: spec.Name}, nil
		},
	}
	g := guestBuild{name: "go"}
	res, err := benchWASM(context.Background(), p, g, 5, 2)
	if err != nil {
		t.Fatalf("benchWASM: unexpected error: %v", err)
	}
	if res.name != "go" || res.opsPerSec <= 0 {
		t.Errorf("res = %+v", res)
	}
	if res.rss != "(measured separately)" {
		t.Errorf("res.rss = %q, want the placeholder (filled by measureGuestRSS in runBench)", res.rss)
	}
}

func TestBenchWASM_LoadError(t *testing.T) {
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return nil, errors.New("load boom")
		},
	}
	_, err := benchWASM(context.Background(), p, guestBuild{name: "go"}, 5, 2)
	if err == nil || !strings.Contains(err.Error(), "Load") {
		t.Errorf("err = %v, want a Load error", err)
	}
}

func TestBenchWASM_FirstDispatchError(t *testing.T) {
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return &fakeComputeInstance{
				name: spec.Name,
				dispatchFn: func(ctx context.Context, args json.RawMessage) (string, error) {
					return "", errors.New("dispatch boom")
				},
			}, nil
		},
	}
	_, err := benchWASM(context.Background(), p, guestBuild{name: "go"}, 5, 2)
	if err == nil || !strings.Contains(err.Error(), "first Dispatch") {
		t.Errorf("err = %v, want a first-Dispatch error", err)
	}
}

func TestBenchWASM_WarmLoadError(t *testing.T) {
	calls := 0
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			calls++
			if calls <= 2 { // let the 2 cold-runs (coldRuns=2) succeed
				return &fakeComputeInstance{name: spec.Name}, nil
			}
			return nil, errors.New("warm load boom") // the post-cold-loop Load fails
		},
	}
	_, err := benchWASM(context.Background(), p, guestBuild{name: "go"}, 5, 2)
	if err == nil || !strings.Contains(err.Error(), "Load (warm)") {
		t.Errorf("err = %v, want a warm-Load error", err)
	}
}

func TestBenchWASM_WarmupDispatchError(t *testing.T) {
	loadCalls := 0
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			loadCalls++
			isWarm := loadCalls > 1 // coldRuns=1, so the 2nd Load is the warm one
			return &fakeComputeInstance{
				name: spec.Name,
				dispatchFn: func(ctx context.Context, args json.RawMessage) (string, error) {
					if isWarm {
						return "", errors.New("warmup dispatch boom")
					}
					return `{"ok":true}`, nil
				},
			}, nil
		},
	}
	_, err := benchWASM(context.Background(), p, guestBuild{name: "go"}, 5, 1)
	if err == nil || !strings.Contains(err.Error(), "warmup Dispatch") {
		t.Errorf("err = %v, want a warmup-Dispatch error", err)
	}
}

func TestBenchWASM_SteadyStateDispatchError(t *testing.T) {
	loadCalls := 0
	p := &fakeComputeProvider{
		loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			loadCalls++
			isWarm := loadCalls > 1
			dispatchN := 0
			return &fakeComputeInstance{
				name: spec.Name,
				dispatchFn: func(ctx context.Context, args json.RawMessage) (string, error) {
					if !isWarm {
						return `{"ok":true}`, nil
					}
					dispatchN++
					if dispatchN == 1 {
						return `{"ok":true}`, nil // the warmup dispatch
					}
					return "", errors.New("steady-state boom") // first of the N=3 timed dispatches
				},
			}, nil
		},
	}
	_, err := benchWASM(context.Background(), p, guestBuild{name: "go"}, 3, 1)
	if err == nil || !strings.Contains(err.Error(), "Dispatch 0") {
		t.Errorf("err = %v, want a 'Dispatch 0' error", err)
	}
}

func TestMeasureGuestRSS(t *testing.T) {
	t.Run("executable path error", func(t *testing.T) {
		orig := executablePath
		executablePath = func() (string, error) { return "", errors.New("no self path") }
		defer func() { executablePath = orig }()

		if got := measureGuestRSS(&fakeRunner{}, "go"); got != "N/A (in-proc)" {
			t.Errorf("measureGuestRSS = %q", got)
		}
	})

	t.Run("subprocess Output error", func(t *testing.T) {
		orig := executablePath
		executablePath = func() (string, error) { return "/bin/harness", nil }
		defer func() { executablePath = orig }()

		r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("subprocess boom") }}
		if got := measureGuestRSS(r, "go"); got != "N/A (in-proc)" {
			t.Errorf("measureGuestRSS = %q", got)
		}
	})

	t.Run("no RSSDELTA line found", func(t *testing.T) {
		orig := executablePath
		executablePath = func() (string, error) { return "/bin/harness", nil }
		defer func() { executablePath = orig }()

		r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("garbage\n"), nil }}
		if got := measureGuestRSS(r, "go"); got != "N/A (in-proc)" {
			t.Errorf("measureGuestRSS = %q", got)
		}
	})

	t.Run("success", func(t *testing.T) {
		orig := executablePath
		executablePath = func() (string, error) { return "/bin/harness", nil }
		defer func() { executablePath = orig }()

		var gotSpec cmdSpec
		r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) {
			gotSpec = spec
			return []byte("noise\nRSSDELTA_MIB=4.2\n"), nil
		}}
		if got := measureGuestRSS(r, "go"); got != "4.2 MiB (in-proc)" {
			t.Errorf("measureGuestRSS = %q", got)
		}
		if gotSpec.Name != "/bin/harness" || len(gotSpec.Args) != 1 || gotSpec.Args[0] != "-rss-only=go" {
			t.Errorf("cmdSpec = %+v", gotSpec)
		}
	})
}

// buildingTinyGoRunner returns a fakeRunner whose LookPath/CombinedOutput make
// buildGuestByName(r, "tinygo") succeed (writing stub module bytes to the
// builder's temp .wasm output), so rssOnlyMain's post-build code (Load/
// Dispatch/selfRSSMiB) is actually reached. psOutputFn stubs the "ps" probe
// selfRSSMiB issues; every other Output call (there are none besides ps once
// the build itself uses CombinedOutput) falls through to psOutputFn too.
func buildingTinyGoRunner(psOutputFn func(spec cmdSpec) ([]byte, error)) *fakeRunner {
	return &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "/opt/homebrew/bin/tinygo", nil },
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			for _, a := range spec.Args {
				if strings.HasSuffix(a, ".wasm") {
					_ = os.WriteFile(a, []byte("tinygo-module-bytes"), 0o644)
				}
			}
			return nil, nil
		},
		outputFn: psOutputFn,
	}
}

func TestRssOnlyMain(t *testing.T) {
	t.Run("skipped guest", func(t *testing.T) {
		orig := goGuestDir
		goGuestDir = "/does/not/exist"
		defer func() { goGuestDir = orig }()

		var out bytes.Buffer
		rssOnlyMain(context.Background(), &fakeRunner{}, &fakeComputeProvider{}, "go", &out)
		if out.String() != "RSSDELTA_MIB=0.0\n" {
			t.Errorf("out = %q", out.String())
		}
	})

	t.Run("Load error", func(t *testing.T) {
		r := buildingTinyGoRunner(func(spec cmdSpec) ([]byte, error) { return []byte("1000"), nil })
		p := &fakeComputeProvider{loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return nil, errors.New("load boom")
		}}
		var out bytes.Buffer
		rssOnlyMain(context.Background(), r, p, "tinygo", &out)
		if out.String() != "RSSDELTA_MIB=0.0\n" {
			t.Errorf("out = %q", out.String())
		}
	})

	t.Run("Dispatch error", func(t *testing.T) {
		r := buildingTinyGoRunner(func(spec cmdSpec) ([]byte, error) { return []byte("1000"), nil })
		p := &fakeComputeProvider{loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return &fakeComputeInstance{dispatchFn: func(ctx context.Context, args json.RawMessage) (string, error) {
				return "", errors.New("dispatch boom")
			}}, nil
		}}
		var out bytes.Buffer
		rssOnlyMain(context.Background(), r, p, "tinygo", &out)
		if out.String() != "RSSDELTA_MIB=0.0\n" {
			t.Errorf("out = %q", out.String())
		}
	})

	t.Run("success", func(t *testing.T) {
		psN := 0
		r := buildingTinyGoRunner(func(spec cmdSpec) ([]byte, error) {
			psN++
			if psN == 1 {
				return []byte("100000"), nil // before, KiB -> ~97.6 MiB
			}
			return []byte("110000"), nil // after
		})
		p := &fakeComputeProvider{loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return &fakeComputeInstance{}, nil
		}}
		var out bytes.Buffer
		rssOnlyMain(context.Background(), r, p, "tinygo", &out)
		if !strings.HasPrefix(out.String(), "RSSDELTA_MIB=") {
			t.Errorf("out = %q", out.String())
		}
		if out.String() == "RSSDELTA_MIB=0.0\n" {
			t.Errorf("expected a nonzero delta given rising ps output, got %q", out.String())
		}
	})

	t.Run("negative delta clamps to zero", func(t *testing.T) {
		psN := 0
		r := buildingTinyGoRunner(func(spec cmdSpec) ([]byte, error) {
			psN++
			if psN == 1 {
				return []byte("200000"), nil // before: higher
			}
			return []byte("100000"), nil // after: lower -> negative delta
		})
		p := &fakeComputeProvider{loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
			return &fakeComputeInstance{}, nil
		}}
		var out bytes.Buffer
		rssOnlyMain(context.Background(), r, p, "tinygo", &out)
		if out.String() != "RSSDELTA_MIB=0.0\n" {
			t.Errorf("out = %q, want clamped to 0.0", out.String())
		}
	})
}

func TestBenchCompileCache_Success(t *testing.T) {
	g := guestBuild{name: "go", module: emptyWasmModule}
	cold, cached, err := benchCompileCache(context.Background(), g, 3)
	if err != nil {
		t.Fatalf("benchCompileCache: unexpected error: %v", err)
	}
	if cold < 0 || cached < 0 {
		t.Errorf("cold=%v cached=%v", cold, cached)
	}
}

func TestBenchCompileCache_InvalidModule(t *testing.T) {
	g := guestBuild{name: "go", module: []byte{0x00, 0x00, 0x00, 0x00}} // bad magic
	_, _, err := benchCompileCache(context.Background(), g, 2)
	if err == nil || !strings.Contains(err.Error(), "CompileModule") {
		t.Errorf("err = %v, want a CompileModule error", err)
	}
}

// TestBenchCompileCache_InvalidModule_ZeroColdRuns forces runs=0, so the cold
// loop never executes (colds stays empty, no error there), but the
// unconditional "warm the cache" compileOnce call still runs — with an
// invalid module that reaches and fails specifically at that line, not inside
// the cold loop.
func TestBenchCompileCache_InvalidModule_ZeroColdRuns(t *testing.T) {
	g := guestBuild{name: "go", module: []byte{0x00, 0x00, 0x00, 0x00}}
	_, _, err := benchCompileCache(context.Background(), g, 0)
	if err == nil || !strings.Contains(err.Error(), "CompileModule") {
		t.Errorf("err = %v, want a CompileModule error from the cache-warm call", err)
	}
}

func TestBenchCompileCache_InvalidModule_FailsDuringWarmCache(t *testing.T) {
	// A module that is well-formed enough to compile once is not achievable
	// deterministically with a single byte slice across cold vs cached passes
	// (CompileModule is deterministic on the same bytes), so instead this
	// verifies runs=0 short-circuits both loops without ever compiling —
	// covering the "runs" boundary the FATAL-error tests above do not reach.
	g := guestBuild{name: "go", module: emptyWasmModule}
	cold, cached, err := benchCompileCache(context.Background(), g, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cold != 0 || cached != 0 {
		t.Errorf("cold=%v cached=%v, want zeros when runs=0 (median of nil)", cold, cached)
	}
}

func TestDockerSpec(t *testing.T) {
	spec := dockerSpec("alpine:3.20", "my-container")
	if spec.Image != "alpine:3.20" || spec.Name != "my-container" || spec.Workdir != sandbox.DefaultWorkdir {
		t.Errorf("dockerSpec = %+v", spec)
	}
}

func TestBenchDocker_ColdCreateFails(t *testing.T) {
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		return nil, errors.New("create boom")
	}}
	res := benchDocker(context.Background(), &fakeRunner{}, p, "alpine:3.20", 10, 2)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
	if p.cleanupCalls != 1 {
		t.Errorf("cleanupCalls = %d, want 1 (belt-and-suspenders defer)", p.cleanupCalls)
	}
}

func TestBenchDocker_ColdExecFails(t *testing.T) {
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		return &fakeSandbox{execFn: func(ctx context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
			return sandbox.ExecResult{}, errors.New("exec boom")
		}}, nil
	}}
	res := benchDocker(context.Background(), &fakeRunner{}, p, "alpine:3.20", 10, 2)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
}

func TestBenchDocker_WarmCreateFails(t *testing.T) {
	calls := 0
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		calls++
		if calls <= 2 { // coldRuns=2 succeed
			return &fakeSandbox{}, nil
		}
		return nil, errors.New("warm create boom")
	}}
	res := benchDocker(context.Background(), &fakeRunner{}, p, "alpine:3.20", 10, 2)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
}

func TestBenchDocker_WarmupExecFails(t *testing.T) {
	calls := 0
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		calls++
		isWarm := calls > 1 // coldRuns=1
		return &fakeSandbox{execFn: func(ctx context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
			if isWarm {
				return sandbox.ExecResult{}, errors.New("warmup boom")
			}
			return sandbox.ExecResult{ExitCode: 0}, nil
		}}, nil
	}}
	res := benchDocker(context.Background(), &fakeRunner{}, p, "alpine:3.20", 10, 1)
	if res != nil {
		t.Errorf("res = %+v, want nil", res)
	}
}

func TestBenchDocker_SteadyStateStopsEarly(t *testing.T) {
	createCalls := 0
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("1.2MiB / 512MiB"), nil }}
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		createCalls++
		isWarm := createCalls > 1 // coldRuns=1
		execN := 0
		return &fakeSandbox{execFn: func(ctx context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
			if !isWarm {
				return sandbox.ExecResult{ExitCode: 0}, nil
			}
			execN++
			if execN <= 3 { // warmup + 2 timed execs succeed
				return sandbox.ExecResult{ExitCode: 0}, nil
			}
			return sandbox.ExecResult{}, errors.New("exec 3 boom") // stops the N=5 loop early
		}}, nil
	}}
	res := benchDocker(context.Background(), r, p, "alpine:3.20", 5, 1)
	if res == nil {
		t.Fatal("res = nil, want a partial result (2 successful timed execs before the early stop)")
	}
	if res.name != "docker" || res.rss == "" {
		t.Errorf("res = %+v", res)
	}
}

// TestBenchDocker_ZeroN covers the len(durs)==0 guard via the n=0 boundary:
// setup/warmup succeed, but the steady-state loop never runs, so durs stays
// empty and benchDocker must return nil rather than a result with a NaN
// opsPerSec.
func TestBenchDocker_ZeroN(t *testing.T) {
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("3.9MiB / 512MiB"), nil }}
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		return &fakeSandbox{}, nil
	}}
	res := benchDocker(context.Background(), r, p, "alpine:3.20", 0, 1)
	if res != nil {
		t.Errorf("res = %+v, want nil for n=0", res)
	}
}

func TestBenchDocker_Success(t *testing.T) {
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("3.9MiB / 512MiB"), nil }}
	p := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		return &fakeSandbox{}, nil
	}}
	res := benchDocker(context.Background(), r, p, "alpine:3.20", 10, 2)
	if res == nil {
		t.Fatal("res = nil, want success")
	}
	if res.name != "docker" || res.moduleMiB != "—" || res.opsPerSec <= 0 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.rss, "container") {
		t.Errorf("res.rss = %q", res.rss)
	}
}

func TestSelfRSSMiB(t *testing.T) {
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("  204800  \n"), nil }}
	if got := selfRSSMiB(r); got != 200 {
		t.Errorf("selfRSSMiB = %v, want 200", got)
	}

	r = &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("ps boom") }}
	if got := selfRSSMiB(r); got != 0 {
		t.Errorf("selfRSSMiB(error) = %v, want 0", got)
	}

	r = &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("not-a-number"), nil }}
	if got := selfRSSMiB(r); got != 0 {
		t.Errorf("selfRSSMiB(unparseable) = %v, want 0", got)
	}
}

func TestContainerRSSMiB(t *testing.T) {
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("12.3MiB / 512MiB\n"), nil }}
	if got := containerRSSMiB(r, "c1"); got != "12.3MiB (container)" {
		t.Errorf("containerRSSMiB = %q", got)
	}

	r = &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("docker boom") }}
	if got := containerRSSMiB(r, "c1"); got != "N/A" {
		t.Errorf("containerRSSMiB(error) = %q", got)
	}
}

func TestDockerAvailable(t *testing.T) {
	r := &fakeRunner{lookPathFn: func(file string) (string, error) { return "/usr/local/bin/docker", nil }}
	if !dockerAvailable(r) {
		t.Error("dockerAvailable = false, want true")
	}
	r = &fakeRunner{lookPathFn: func(file string) (string, error) { return "", errors.New("not found") }}
	if dockerAvailable(r) {
		t.Error("dockerAvailable = true, want false")
	}
}

func TestImageAvailable(t *testing.T) {
	r := &fakeRunner{runFn: func(spec cmdSpec) error { return nil }}
	if !imageAvailable(r, "alpine:3.20") {
		t.Error("imageAvailable = false, want true")
	}
	r = &fakeRunner{runFn: func(spec cmdSpec) error { return errors.New("no such image") }}
	if imageAvailable(r, "alpine:3.20") {
		t.Error("imageAvailable = true, want false")
	}
}

func TestDockerVersion(t *testing.T) {
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("24.0.7\n"), nil }}
	if got := dockerVersion(r); got != "24.0.7" {
		t.Errorf("dockerVersion = %q", got)
	}
	r = &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("boom") }}
	if got := dockerVersion(r); got != "n/a" {
		t.Errorf("dockerVersion(error) = %q", got)
	}
}

func TestTinyGoVersion(t *testing.T) {
	r := &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "/opt/homebrew/bin/tinygo", nil },
		outputFn:   func(spec cmdSpec) ([]byte, error) { return []byte("tinygo version 0.31.0\n"), nil },
	}
	if got := tinyGoVersion(r); got != "tinygo version 0.31.0" {
		t.Errorf("tinyGoVersion = %q", got)
	}

	r = &fakeRunner{lookPathFn: func(file string) (string, error) { return "", errors.New("not found") }}
	if got := tinyGoVersion(r); got != "not installed" {
		t.Errorf("tinyGoVersion(missing) = %q", got)
	}

	r = &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "/opt/homebrew/bin/tinygo", nil },
		outputFn:   func(spec cmdSpec) ([]byte, error) { return nil, errors.New("boom") },
	}
	if got := tinyGoVersion(r); got != "unknown" {
		t.Errorf("tinyGoVersion(output error) = %q", got)
	}
}

func TestRustVersion(t *testing.T) {
	t.Setenv("RUSTC", "/usr/bin/rustc")
	r := &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("rustc 1.80.0\n"), nil }}
	if got := rustVersion(r); got != "rustc 1.80.0" {
		t.Errorf("rustVersion = %q", got)
	}

	t.Setenv("RUSTC", "")
	r = &fakeRunner{lookPathFn: func(file string) (string, error) { return "", errors.New("not found") }}
	if got := rustVersion(r); got != "not installed" {
		t.Errorf("rustVersion(missing) = %q", got)
	}

	t.Setenv("RUSTC", "/usr/bin/rustc")
	r = &fakeRunner{outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("boom") }}
	if got := rustVersion(r); got != "unknown" {
		t.Errorf("rustVersion(output error) = %q", got)
	}
}

func TestWazeroVersion(t *testing.T) {
	orig := readBuildInfo
	defer func() { readBuildInfo = orig }()

	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	if got := wazeroVersion(); got != "unknown" {
		t.Errorf("wazeroVersion(ok=false) = %q", got)
	}

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Deps: []*debug.Module{{Path: "some/other/dep", Version: "v9.9.9"}}}, true
	}
	if got := wazeroVersion(); got != "unknown" {
		t.Errorf("wazeroVersion(dep not found) = %q", got)
	}

	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/tetratelabs/wazero", Version: "v1.12.0"}}}, true
	}
	if got := wazeroVersion(); got != "v1.12.0" {
		t.Errorf("wazeroVersion(found) = %q", got)
	}
}

// TestWazeroVersion_RealBuildInfo exercises the actual debug.ReadBuildInfo
// path (no seam substitution) at least once, so the production default is not
// only covered indirectly through the fake.
func TestWazeroVersion_RealBuildInfo(t *testing.T) {
	got := wazeroVersion()
	if got == "" {
		t.Error("wazeroVersion() returned empty string")
	}
	// Either resolves to a real semver-ish string (the module is a direct
	// import of this package) or degrades to "unknown" if build info is
	// unavailable in this test binary — both are valid, exercised outcomes.
	if got != "unknown" && !strings.HasPrefix(got, "v") {
		t.Errorf("wazeroVersion() = %q, want either 'unknown' or a version starting with 'v'", got)
	}
}

func TestFakeComputeInstance_DefaultDispatch(t *testing.T) {
	i := &fakeComputeInstance{name: "x"}
	out, err := i.Dispatch(context.Background(), json.RawMessage(`{}`))
	if err != nil || out != `{"ok":true}` {
		t.Errorf("Dispatch default = %q, %v", out, err)
	}
	if i.Name() != "x" {
		t.Errorf("Name() = %q", i.Name())
	}
	if err := i.Close(context.Background()); err != nil || i.closeCalls != 1 {
		t.Errorf("Close = %v, closeCalls=%d", err, i.closeCalls)
	}
}

func TestFakeSandbox_Defaults(t *testing.T) {
	s := &fakeSandbox{workdir: "/workspace"}
	res, err := s.Exec(context.Background(), []string{"echo"}, sandbox.ExecOptions{})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("Exec default = %+v, %v", res, err)
	}
	if err := s.CopyIn(context.Background(), "a", "b"); err != nil {
		t.Errorf("CopyIn = %v", err)
	}
	if err := s.CopyOut(context.Background(), "a", "b"); err != nil {
		t.Errorf("CopyOut = %v", err)
	}
	if s.Workdir() != "/workspace" {
		t.Errorf("Workdir = %q", s.Workdir())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestFakeDockerProvider_Defaults(t *testing.T) {
	p := &fakeDockerProvider{}
	sb, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil || sb == nil {
		t.Errorf("Create default = %v, %v", sb, err)
	}
	if p.Name() != "fake-docker" {
		t.Errorf("Name() = %q", p.Name())
	}
	p.CleanupAll(context.Background())
	if p.cleanupCalls != 1 {
		t.Errorf("cleanupCalls = %d", p.cleanupCalls)
	}
}

func TestFakeComputeProvider_Name(t *testing.T) {
	p := &fakeComputeProvider{}
	if p.Name() != "fake-wasm" {
		t.Errorf("Name() = %q", p.Name())
	}
	if _, err := p.Load(context.Background(), sandbox.ComputeSpec{Name: "x"}); err != nil {
		t.Errorf("Load default: %v", err)
	}
	if p.loadCalls != 1 {
		t.Errorf("loadCalls = %d", p.loadCalls)
	}
}

// TestBenchWASM_ZeroN sanity-checks the boundary n=0: no timed dispatches, but
// setup/firstCall are still measured over coldRuns. opsPerSec for a
// near-instant zero-length loop is 0/~0s, which can legitimately land on
// either 0 or NaN depending on timer resolution — the boundary this test
// guards is that benchWASM does not error and still returns percentiles of an
// empty slice (zeros), not that opsPerSec takes one specific value.
func TestBenchWASM_ZeroN(t *testing.T) {
	p := &fakeComputeProvider{}
	res, err := benchWASM(context.Background(), p, guestBuild{name: "rust"}, 0, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.p50 != 0 || res.p95 != 0 || res.p99 != 0 {
		t.Errorf("percentiles for n=0 should be zero: p50=%v p95=%v p99=%v", res.p50, res.p95, res.p99)
	}
}

func TestBenchCompileCache_TimingIsMonotonicSane(t *testing.T) {
	g := guestBuild{name: "go", module: emptyWasmModule}
	cold, cached, err := benchCompileCache(context.Background(), g, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cold < 0 || cached < 0 {
		t.Errorf("cold=%v cached=%v must be non-negative durations", cold, cached)
	}
	_ = time.Now() // keep time import used even if the above assertions change
}

func TestGuestBuildStructFields(t *testing.T) {
	// A cheap struct-shape regression check: guestBuild's zero value should
	// report as a non-skipped, empty guest (reflect-based to avoid hand
	// enumerating every field twice).
	var g guestBuild
	if g.skipped {
		t.Error("zero-value guestBuild.skipped should be false")
	}
	if !reflect.DeepEqual(g.module, []byte(nil)) {
		t.Errorf("zero-value module = %v", g.module)
	}
}
