package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWasiNote(t *testing.T) {
	if got := wasiNote(true); got != "; imports WASI" {
		t.Errorf("wasiNote(true) = %q", got)
	}
	if got := wasiNote(false); got != "; imports nothing" {
		t.Errorf("wasiNote(false) = %q", got)
	}
}

func TestFinishGuest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guest.wasm")
	if err := os.WriteFile(path, []byte("wasm-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	g := finishGuest(guestBuild{name: "go"}, path)
	if g.skipped {
		t.Fatalf("finishGuest(existing file) skipped: %s", g.skipReason)
	}
	if string(g.module) != "wasm-bytes" || g.sizeBytes != len("wasm-bytes") {
		t.Errorf("finishGuest module/sizeBytes = %q/%d", g.module, g.sizeBytes)
	}

	g = finishGuest(guestBuild{name: "go"}, filepath.Join(dir, "missing.wasm"))
	if !g.skipped {
		t.Error("finishGuest(missing file) expected skipped=true")
	}
}

func TestBuildGuestByName_Unknown(t *testing.T) {
	g := buildGuestByName(&fakeRunner{}, "cobol")
	if !g.skipped || g.skipReason != "unknown guest name" {
		t.Errorf("buildGuestByName(unknown) = %+v", g)
	}
}

func TestBuildGoGuest_DirMissing(t *testing.T) {
	orig := goGuestDir
	goGuestDir = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { goGuestDir = orig }()

	g := buildGoGuest(&fakeRunner{})
	if !g.skipped {
		t.Fatal("expected skipped=true when guest dir is missing")
	}
	if !strings.Contains(g.skipReason, "not found") {
		t.Errorf("skipReason = %q, want it to mention 'not found'", g.skipReason)
	}
}

func TestBuildGoGuest_CreateTempFails(t *testing.T) {
	orig := goGuestDir
	goGuestDir = t.TempDir()
	defer func() { goGuestDir = orig }()

	origTemp := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) { return nil, errors.New("no space left on device") }
	defer func() { createTemp = origTemp }()

	g := buildGoGuest(&fakeRunner{})
	if !g.skipped || g.skipReason != "no space left on device" {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildGoGuest_BuildFails(t *testing.T) {
	orig := goGuestDir
	goGuestDir = t.TempDir() // exists, so os.Stat succeeds
	defer func() { goGuestDir = orig }()

	r := &fakeRunner{
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			if spec.Name != "go" {
				t.Errorf("unexpected command %q", spec.Name)
			}
			return []byte("build error output"), errors.New("exit status 1")
		},
	}
	g := buildGoGuest(r)
	if !g.skipped {
		t.Fatal("expected skipped=true when go build fails")
	}
	if !strings.Contains(g.skipReason, "go build") || !strings.Contains(g.skipReason, "build error output") {
		t.Errorf("skipReason = %q", g.skipReason)
	}
}

func TestBuildGoGuest_Success(t *testing.T) {
	orig := goGuestDir
	goGuestDir = t.TempDir()
	defer func() { goGuestDir = orig }()

	var gotSpec cmdSpec
	r := &fakeRunner{
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			gotSpec = spec
			// Simulate the compiler writing the module to -o <path>: the last
			// arg before "." is the output path in the real invocation, but to
			// keep this decoupled from the exact arg order we just write to
			// every arg that looks like a temp .wasm file.
			for _, a := range spec.Args {
				if strings.HasSuffix(a, ".wasm") {
					if err := os.WriteFile(a, []byte("go-module-bytes"), 0o644); err != nil {
						t.Fatalf("stub write: %v", err)
					}
				}
			}
			return nil, nil
		},
	}
	g := buildGoGuest(r)
	if g.skipped {
		t.Fatalf("expected success, got skipped: %s", g.skipReason)
	}
	if g.name != "go" || !g.allowWASI {
		t.Errorf("g = %+v", g)
	}
	if string(g.module) != "go-module-bytes" {
		t.Errorf("module = %q", g.module)
	}
	if gotSpec.Name != "go" || gotSpec.Dir != goGuestDir {
		t.Errorf("cmdSpec = %+v", gotSpec)
	}
	foundEnv := false
	for _, e := range gotSpec.Env {
		if e == "GOOS=wasip1" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("expected GOOS=wasip1 in Env, got %v", gotSpec.Env)
	}
}

func TestBuildTinyGoGuest_NotOnPath(t *testing.T) {
	r := &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "", errors.New("not found") },
	}
	g := buildTinyGoGuest(r)
	if !g.skipped || !strings.Contains(g.skipReason, "tinygo") {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildTinyGoGuest_CreateTempFails(t *testing.T) {
	origTemp := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) { return nil, errors.New("no space left on device") }
	defer func() { createTemp = origTemp }()

	r := &fakeRunner{lookPathFn: func(file string) (string, error) { return "/usr/local/bin/tinygo", nil }}
	g := buildTinyGoGuest(r)
	if !g.skipped || g.skipReason != "no space left on device" {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildTinyGoGuest_BuildFails(t *testing.T) {
	r := &fakeRunner{
		lookPathFn:       func(file string) (string, error) { return "/usr/local/bin/tinygo", nil },
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) { return []byte("tinygo error"), errors.New("exit 1") },
	}
	g := buildTinyGoGuest(r)
	if !g.skipped || !strings.Contains(g.skipReason, "tinygo build") {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildTinyGoGuest_Success(t *testing.T) {
	r := &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "/usr/local/bin/tinygo", nil },
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			for _, a := range spec.Args {
				if strings.HasSuffix(a, ".wasm") {
					if err := os.WriteFile(a, []byte("tinygo-bytes"), 0o644); err != nil {
						t.Fatalf("stub write: %v", err)
					}
				}
			}
			return nil, nil
		},
	}
	g := buildTinyGoGuest(r)
	if g.skipped {
		t.Fatalf("expected success, got skipped: %s", g.skipReason)
	}
	if string(g.module) != "tinygo-bytes" {
		t.Errorf("module = %q", g.module)
	}
}

func TestResolveRustc(t *testing.T) {
	t.Run("RUSTC env wins", func(t *testing.T) {
		t.Setenv("RUSTC", "/opt/rustc-pinned")
		if got := resolveRustc(&fakeRunner{}); got != "/opt/rustc-pinned" {
			t.Errorf("resolveRustc = %q", got)
		}
	})

	t.Run("rustup which rustc", func(t *testing.T) {
		t.Setenv("RUSTC", "")
		r := &fakeRunner{
			lookPathFn: func(file string) (string, error) {
				if file == "rustup" {
					return "/usr/local/bin/rustup", nil
				}
				return "", errors.New("not found")
			},
			outputFn: func(spec cmdSpec) ([]byte, error) {
				return []byte("/home/user/.rustup/toolchains/stable/bin/rustc\n"), nil
			},
		}
		if got := resolveRustc(r); got != "/home/user/.rustup/toolchains/stable/bin/rustc" {
			t.Errorf("resolveRustc = %q", got)
		}
	})

	t.Run("rustup which rustc empty output falls through to bare rustc", func(t *testing.T) {
		t.Setenv("RUSTC", "")
		r := &fakeRunner{
			lookPathFn: func(file string) (string, error) {
				switch file {
				case "rustup":
					return "/usr/local/bin/rustup", nil
				case "rustc":
					return "/usr/bin/rustc", nil
				}
				return "", errors.New("not found")
			},
			outputFn: func(spec cmdSpec) ([]byte, error) { return []byte("  \n"), nil },
		}
		if got := resolveRustc(r); got != "/usr/bin/rustc" {
			t.Errorf("resolveRustc = %q", got)
		}
	})

	t.Run("rustup which rustc errors falls through to bare rustc", func(t *testing.T) {
		t.Setenv("RUSTC", "")
		r := &fakeRunner{
			lookPathFn: func(file string) (string, error) {
				switch file {
				case "rustup":
					return "/usr/local/bin/rustup", nil
				case "rustc":
					return "/usr/bin/rustc", nil
				}
				return "", errors.New("not found")
			},
			outputFn: func(spec cmdSpec) ([]byte, error) { return nil, errors.New("rustup broken") },
		}
		if got := resolveRustc(r); got != "/usr/bin/rustc" {
			t.Errorf("resolveRustc = %q", got)
		}
	})

	t.Run("nothing available", func(t *testing.T) {
		t.Setenv("RUSTC", "")
		r := &fakeRunner{
			lookPathFn: func(file string) (string, error) { return "", errors.New("not found") },
		}
		if got := resolveRustc(r); got != "" {
			t.Errorf("resolveRustc = %q, want empty", got)
		}
	})
}

func TestBuildRustGuest_NoRustc(t *testing.T) {
	t.Setenv("RUSTC", "")
	r := &fakeRunner{lookPathFn: func(file string) (string, error) { return "", errors.New("not found") }}
	g := buildRustGuest(r)
	if !g.skipped || !strings.Contains(g.skipReason, "no rustc") {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildRustGuest_SrcMissing(t *testing.T) {
	t.Setenv("RUSTC", "/usr/bin/rustc")
	orig := rustGuestDir
	rustGuestDir = filepath.Join(t.TempDir(), "no-such-dir")
	defer func() { rustGuestDir = orig }()

	g := buildRustGuest(&fakeRunner{})
	if !g.skipped || !strings.Contains(g.skipReason, "not found") {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildRustGuest_CreateTempFails(t *testing.T) {
	t.Setenv("RUSTC", "/usr/bin/rustc")
	orig := rustGuestDir
	rustGuestDir = t.TempDir()
	defer func() { rustGuestDir = orig }()
	if err := os.WriteFile(filepath.Join(rustGuestDir, "guest.rs"), []byte("// guest"), 0o644); err != nil {
		t.Fatal(err)
	}

	origTemp := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) { return nil, errors.New("no space left on device") }
	defer func() { createTemp = origTemp }()

	g := buildRustGuest(&fakeRunner{})
	if !g.skipped || g.skipReason != "no space left on device" {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildRustGuest_BuildFails(t *testing.T) {
	t.Setenv("RUSTC", "/usr/bin/rustc")
	orig := rustGuestDir
	rustGuestDir = t.TempDir()
	defer func() { rustGuestDir = orig }()
	if err := os.WriteFile(filepath.Join(rustGuestDir, "guest.rs"), []byte("// guest"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) { return []byte("rustc error"), errors.New("exit 1") },
	}
	g := buildRustGuest(r)
	if !g.skipped || !strings.Contains(g.skipReason, "rustc") {
		t.Errorf("g = %+v", g)
	}
}

func TestBuildRustGuest_Success(t *testing.T) {
	t.Setenv("RUSTC", "/usr/bin/rustc")
	orig := rustGuestDir
	rustGuestDir = t.TempDir()
	defer func() { rustGuestDir = orig }()
	if err := os.WriteFile(filepath.Join(rustGuestDir, "guest.rs"), []byte("// guest"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			for _, a := range spec.Args {
				if strings.HasSuffix(a, ".wasm") {
					if err := os.WriteFile(a, []byte("rust-bytes"), 0o644); err != nil {
						t.Fatalf("stub write: %v", err)
					}
				}
			}
			return nil, nil
		},
	}
	g := buildRustGuest(r)
	if g.skipped {
		t.Fatalf("expected success, got skipped: %s", g.skipReason)
	}
	if g.allowWASI {
		t.Error("rust guest must have allowWASI=false (imports nothing)")
	}
	if string(g.module) != "rust-bytes" {
		t.Errorf("module = %q", g.module)
	}
}

func TestBuildGuestByName_Dispatch(t *testing.T) {
	orig := goGuestDir
	goGuestDir = filepath.Join(t.TempDir(), "missing")
	defer func() { goGuestDir = orig }()

	g := buildGuestByName(&fakeRunner{lookPathFn: func(string) (string, error) { return "", errors.New("nope") }}, "go")
	if g.name != "go" {
		t.Errorf("buildGuestByName(go) = %+v", g)
	}

	g = buildGuestByName(&fakeRunner{lookPathFn: func(string) (string, error) { return "", errors.New("nope") }}, "tinygo")
	if g.name != "tinygo" {
		t.Errorf("buildGuestByName(tinygo) = %+v", g)
	}

	t.Setenv("RUSTC", "")
	g = buildGuestByName(&fakeRunner{lookPathFn: func(string) (string, error) { return "", errors.New("nope") }}, "rust")
	if g.name != "rust" {
		t.Errorf("buildGuestByName(rust) = %+v", g)
	}
}
