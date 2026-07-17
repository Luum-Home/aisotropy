package sandbox

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// requirePodman skips unless a working podman CLI is present, mirroring the
// docker/rqlite/qdrant/sleepcycle "skip, never fake" gate. Podman is commonly
// absent on macOS/CI, so these real-container cases will typically Skip here.
func requirePodman(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !podmanAvailable(ctx, "") {
		t.Skip("podman not available (podman version failed); skipping real-container tests")
	}
}

// argvContains reports whether argv contains want as an exact element.
func argvContains(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// TestPodmanRunArgs_RuntimeKnob asserts the --runtime flag is emitted only when
// Spec.Runtime is set — the OCI-runtime knob wiring — without needing podman.
func TestPodmanRunArgs_RuntimeKnob(t *testing.T) {
	p := NewPodmanProvider()

	// Runtime set → --runtime=<value> present.
	withRT := p.podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine", Runtime: "kata"})
	if !argvContains(withRT, "--runtime=kata") {
		t.Fatalf("expected --runtime=kata in argv, got %v", withRT)
	}

	// Runtime empty → no --runtime flag at all.
	noRT := p.podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine"})
	for _, a := range noRT {
		if strings.HasPrefix(a, "--runtime") {
			t.Fatalf("expected no --runtime flag when Runtime empty, got %v", noRT)
		}
	}
}

// TestPodmanRunArgs_Rootless asserts the rootless userns mapping: pinned to
// uid/gid when Spec.User is a pair, plain keep-id otherwise, and absent when the
// provider is not rootless.
func TestPodmanRunArgs_Rootless(t *testing.T) {
	p := NewPodmanProvider() // Rootless defaults true

	// User pinned → keep-id:uid=..,gid=..
	pinned := p.podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine", User: "1000:1000"})
	if !argvContains(pinned, "--userns=keep-id:uid=1000,gid=1000") {
		t.Fatalf("expected pinned keep-id in argv, got %v", pinned)
	}
	if !argvContains(pinned, "--user") {
		t.Fatalf("expected --user flag alongside keep-id, got %v", pinned)
	}

	// No User → plain keep-id.
	bare := p.podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine"})
	if !argvContains(bare, "--userns=keep-id") {
		t.Fatalf("expected plain keep-id in argv, got %v", bare)
	}

	// Rootful provider → no userns flag at all.
	rootful := &PodmanProvider{Rootless: false}
	ra := rootful.podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine", User: "1000:1000"})
	for _, a := range ra {
		if strings.HasPrefix(a, "--userns") {
			t.Fatalf("expected no --userns when not rootless, got %v", ra)
		}
	}
}

// TestPodmanRunArgs_EntrypointSleep asserts podman overrides the image
// entrypoint to sleep and passes infinity as the argument (the podman-specific
// idle-forever wiring).
func TestPodmanRunArgs_EntrypointSleep(t *testing.T) {
	argv := NewPodmanProvider().podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine:3.20"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--entrypoint sleep alpine:3.20 infinity") {
		t.Fatalf("expected `--entrypoint sleep alpine:3.20 infinity` tail, got %v", argv)
	}
}

// TestPodmanRunArgs_HardeningDefaults proves the shared secure-by-default flags
// are emitted by the podman builder too, composing WITH the rootless keep-id
// mapping (defense in depth: user-namespace isolation plus cap-drop/read-only/
// resource caps). No podman install needed.
func TestPodmanRunArgs_HardeningDefaults(t *testing.T) {
	argv := NewPodmanProvider().podmanRunArgs("box", DefaultWorkdir, Spec{Image: "alpine"})

	// Rootless mapping is preserved.
	if !argvContains(argv, "--userns=keep-id") {
		t.Errorf("rootless keep-id must remain, got %v", argv)
	}
	// Hardening defaults are all present.
	for _, want := range []string{"--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only"} {
		if !argvContains(argv, want) {
			t.Errorf("expected %s by default, got %v", want, argv)
		}
	}
	if !argvPair(argv, "--tmpfs", scratchTmpfs) {
		t.Errorf("expected --tmpfs %s, got %v", scratchTmpfs, argv)
	}
	if !argvPair(argv, "--pids-limit", strconv.Itoa(DefaultPidsLimit)) {
		t.Errorf("expected --pids-limit %d, got %v", DefaultPidsLimit, argv)
	}
	if !argvPair(argv, "--memory", DefaultMemory) || !argvPair(argv, "--cpus", DefaultCPUs) {
		t.Errorf("expected --memory %s and --cpus %s, got %v", DefaultMemory, DefaultCPUs, argv)
	}
	// The entrypoint-sleep tail is preserved after all hardening flags.
	if !strings.Contains(strings.Join(argv, " "), "--entrypoint sleep alpine infinity") {
		t.Errorf("expected `--entrypoint sleep alpine infinity` tail, got %v", argv)
	}
}

// TestPodmanRunArgs_HardeningOverridable proves the podman builder honors the
// same widen-when-needed overrides as docker.
func TestPodmanRunArgs_HardeningOverridable(t *testing.T) {
	argv := NewPodmanProvider().podmanRunArgs("box", DefaultWorkdir, Spec{
		Image:    "alpine",
		Network:  "bridge",
		Writable: true,
		Memory:   "1g",
	})
	if !argvContains(argv, "--network=bridge") || argvContains(argv, "--network=none") {
		t.Errorf("network override failed, got %v", argv)
	}
	if argvContains(argv, "--read-only") {
		t.Errorf("Writable=true should drop --read-only, got %v", argv)
	}
	if !argvPair(argv, "--memory", "1g") {
		t.Errorf("expected --memory 1g override, got %v", argv)
	}
}

// TestSplitUserSpec covers the "uid:gid" parser feeding keepIDArg.
func TestSplitUserSpec(t *testing.T) {
	cases := []struct {
		in         string
		uid, gid   string
		ok         bool
		keepIDWant string
	}{
		{"1000:1000", "1000", "1000", true, "keep-id:uid=1000,gid=1000"},
		{"0:0", "0", "0", true, "keep-id:uid=0,gid=0"},
		{"", "", "", false, "keep-id"},
		{"1000", "", "", false, "keep-id"},
		{"1000:", "", "", false, "keep-id"},
		{":1000", "", "", false, "keep-id"},
	}
	for _, c := range cases {
		uid, gid, ok := splitUserSpec(c.in)
		if ok != c.ok || uid != c.uid || gid != c.gid {
			t.Errorf("splitUserSpec(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, uid, gid, ok, c.uid, c.gid, c.ok)
		}
		if got := keepIDArg(c.in); got != c.keepIDWant {
			t.Errorf("keepIDArg(%q) = %q, want %q", c.in, got, c.keepIDWant)
		}
	}
}

// TestPodman_ExecEmptyArgvErrors is a pure guard-path test — no daemon needed.
func TestPodman_ExecEmptyArgvErrors(t *testing.T) {
	sb := &podmanSandbox{provider: NewPodmanProvider(), name: "x", workdir: "/w"}
	if _, err := sb.Exec(context.Background(), nil, ExecOptions{}); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

// TestPodman_RoundTrip mirrors the Docker round-trip against podman. It Skips
// when podman is absent (the common case here) rather than faking a container.
func TestPodman_RoundTrip(t *testing.T) {
	requirePodman(t)
	p := NewPodmanProvider()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := p.run(ctx, "pull", testImage()); err != nil {
		t.Skipf("cannot pull %s (offline?): %v", testImage(), err)
	}

	spec := Spec{
		Image:   testImage(),
		Workdir: DefaultWorkdir,
		Env:     map[string]string{"AISOTROPY_TEST": "1"},
	}
	sb, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = sb.Close(cctx)
	})

	if sb.Workdir() != DefaultWorkdir {
		t.Fatalf("Workdir() = %q, want %q", sb.Workdir(), DefaultWorkdir)
	}

	var mu sync.Mutex
	var streamed []string
	res, err := sb.Exec(ctx, []string{"sh", "-c", "printf 'a\\nb\\n'"}, ExecOptions{
		OnLine: func(l string) { mu.Lock(); streamed = append(streamed, l); mu.Unlock() },
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.Join(streamed, ",") != "a,b" {
		t.Fatalf("streamed = %v, want [a b]", streamed)
	}

	// Non-zero exit is a result, not an error.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "exit 7"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec exit7 returned Go error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
}
