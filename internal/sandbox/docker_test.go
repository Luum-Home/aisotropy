package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// argvPair reports whether argv contains the adjacent pair [flag, value] as two
// consecutive elements — the "--flag value" (space-separated) form docker/podman
// use for --tmpfs/--pids-limit/--memory/--cpus.
func argvPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// testImage is a tiny stock image with sh. Overridable so a fully-offline CI
// can point at an image it has cached.
func testImage() string {
	if v := os.Getenv("AISOTROPY_SANDBOX_TEST_IMAGE"); v != "" {
		return v
	}
	return "alpine:3.20"
}

// requireDocker skips the test unless a working docker CLI + daemon is present,
// mirroring the rqlite/qdrant/sleepcycle "skip, never fake" gate.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !dockerAvailable(ctx, "") {
		t.Skip("docker not available (docker version failed); skipping real-container tests")
	}
}

// createSandbox stands up a sandbox on testImage, skipping (not failing) if the
// image cannot be pulled — the honest offline path.
func createSandbox(t *testing.T, p *DockerProvider, spec Spec) Sandbox {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Pre-pull so an offline "image not found" becomes a clean Skip rather
	// than a confusing Create failure.
	if _, err := p.run(ctx, "pull", spec.Image); err != nil {
		t.Skipf("cannot pull %s (offline?): %v", spec.Image, err)
	}
	sb, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return sb
}

func TestDocker_RoundTrip(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()

	// Bind-mount a host work tree at the default workdir, as a task runner
	// would mount a git worktree.
	host := t.TempDir()
	spec := Spec{
		Image:      testImage(),
		Workdir:    DefaultWorkdir,
		BindMounts: []Mount{{Source: host, Target: DefaultWorkdir}},
		Env:        map[string]string{"AISOTROPY_TEST": "1"},
	}
	sb := createSandbox(t, p, spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Close(ctx)
	})

	ctx := context.Background()

	if sb.Workdir() != DefaultWorkdir {
		t.Fatalf("Workdir() = %q, want %q", sb.Workdir(), DefaultWorkdir)
	}

	// 1) Exec echo, capturing streamed lines AND the buffered result.
	var mu sync.Mutex
	var streamed []string
	res, err := sb.Exec(ctx, []string{"sh", "-c", "printf 'line1\\nline2\\nline3\\n'"}, ExecOptions{
		OnLine: func(l string) {
			mu.Lock()
			streamed = append(streamed, l)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Exec echo: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	wantLines := []string{"line1", "line2", "line3"}
	if strings.Join(streamed, ",") != strings.Join(wantLines, ",") {
		t.Fatalf("streamed lines = %v, want %v", streamed, wantLines)
	}
	if res.Stdout != "line1\nline2\nline3\n" {
		t.Fatalf("buffered stdout = %q", res.Stdout)
	}

	// 2) Non-secret env reached the process; provider key never would.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "echo $AISOTROPY_TEST"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec env: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Fatalf("env not propagated: stdout=%q", res.Stdout)
	}

	// 3) A non-zero command exit is a result, not a Go error.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "exit 7"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec exit7 returned Go error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}

	// 4) CopyIn a host file, read it back inside the sandbox.
	//
	// The stage target is the WRITABLE WORKTREE bind mount (Workdir), not a rootfs
	// path like /tmp. This is deliberate and reflects the secure default: with the
	// container rootfs mounted --read-only, `docker cp` INTO the container refuses
	// any destination on the rootfs (even a tmpfs mount) with "container rootfs is
	// marked read-only" — a documented Docker limitation, not ours. `docker cp`
	// into a bind-mounted volume (the worktree) works fine, and the worktree is
	// exactly where a task runner stages inputs. (The real task path stages via
	// the worktree + write_file Exec and never CopyIns to rootfs; runtime Exec
	// writes to /tmp still work — only `docker cp` to /tmp is refused.)
	local := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(local, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.CopyIn(ctx, local, DefaultWorkdir+"/in.txt"); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}
	res, err = sb.Exec(ctx, []string{"cat", DefaultWorkdir + "/in.txt"}, ExecOptions{})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("cat copied file: err=%v exit=%d", err, res.ExitCode)
	}
	if res.Stdout != "payload" && strings.TrimSpace(res.Stdout) != "payload" {
		t.Fatalf("copied content = %q", res.Stdout)
	}

	// 5) Write a file inside the sandbox, CopyOut to the host.
	//
	// Same read-only interaction as step 4, in reverse: under --read-only,
	// `docker cp` OUT also only traverses BIND-MOUNT paths — a cp from a tmpfs
	// path (/tmp) reports "Could not find the file" even though the file exists
	// at runtime. So the CopyOut source is a worktree path, the realistic place a
	// run leaves an artifact. (Runtime Exec to /tmp still works; only `docker cp`
	// is constrained by read-only. Set Spec.Writable to lift the constraint.)
	if _, err := sb.Exec(ctx, []string{"sh", "-c", "echo out > " + DefaultWorkdir + "/out.txt"}, ExecOptions{}); err != nil {
		t.Fatalf("write out.txt: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.txt")
	if err := sb.CopyOut(ctx, DefaultWorkdir+"/out.txt", dst); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copied-out file: %v", err)
	}
	if strings.TrimSpace(string(got)) != "out" {
		t.Fatalf("copied-out content = %q", got)
	}

	// 6) The bind mount is bidirectional: a file written in the sandbox
	// workdir appears on the host mount source.
	if _, err := sb.Exec(ctx, []string{"sh", "-c", "echo shared > shared.txt"}, ExecOptions{}); err != nil {
		t.Fatalf("write shared.txt: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(host, "shared.txt")); err != nil {
		t.Fatalf("shared file not on host mount: %v", err)
	} else if strings.TrimSpace(string(b)) != "shared" {
		t.Fatalf("shared content = %q", b)
	}
}

// TestDocker_HardeningEnforcedAtRuntime proves the secure defaults are not just
// present in the argv but ACTUALLY ENFORCED by the runtime: the rootfs is
// read-only (a write to /etc fails), the worktree bind mount stays writable, and
// there is no network (no loopback route to reach a co-located service). Skips
// without a docker host.
func TestDocker_HardeningEnforcedAtRuntime(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()

	host := t.TempDir()
	spec := Spec{
		Image:      testImage(),
		Workdir:    DefaultWorkdir,
		BindMounts: []Mount{{Source: host, Target: DefaultWorkdir}},
	}
	sb := createSandbox(t, p, spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Close(ctx)
	})
	ctx := context.Background()

	// Rootfs is read-only: writing outside the worktree/tmp fails.
	res, err := sb.Exec(ctx, []string{"sh", "-c", "echo x > /etc/aisotropy_probe"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("expected write to read-only rootfs (/etc) to FAIL, but it succeeded")
	}

	// The worktree bind mount stays writable under --read-only.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "echo ok > work.txt"}, ExecOptions{})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("expected writable worktree, got err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}

	// No network: there is no non-loopback interface to reach a co-located
	// rqlite/Qdrant on. `ip addr`/`ifconfig` may be absent on alpine, so probe
	// via /proc/net/dev: with --network=none only the `lo` interface exists.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "grep -c ':' /proc/net/dev | cat; grep -o 'eth[0-9]' /proc/net/dev | cat"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec net probe: %v", err)
	}
	if strings.Contains(res.Stdout, "eth") {
		t.Errorf("expected no eth interface under --network=none, got stdout=%q", res.Stdout)
	}
}

func TestDocker_CloseIsIdempotentAndRemovesContainer(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()
	spec := Spec{Image: testImage(), Workdir: DefaultWorkdir, Name: "close-test"}
	sb := createSandbox(t, p, spec)

	ctx := context.Background()
	// Container is tracked while live.
	p.mu.Lock()
	tracked := len(p.running)
	p.mu.Unlock()
	if tracked == 0 {
		t.Fatal("expected a tracked running sandbox")
	}

	if err := sb.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close is a no-op, no error.
	if err := sb.Close(ctx); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected 0 tracked sandboxes after Close, got %d", remaining)
	}

	// The container name is gone: docker inspect fails.
	if _, err := p.run(ctx, "inspect", "aisotropy-sbx-close-test"); err == nil {
		t.Fatal("expected docker inspect to fail on a removed container")
	}
}

func TestDocker_CleanupAll(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()
	_ = createSandbox(t, p, Spec{Image: testImage(), Workdir: DefaultWorkdir})
	_ = createSandbox(t, p, Spec{Image: testImage(), Workdir: DefaultWorkdir})

	ctx := context.Background()
	p.CleanupAll(ctx)

	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("CleanupAll left %d sandboxes tracked", remaining)
	}
}

func TestDocker_ExecEmptyArgvErrors(t *testing.T) {
	// Pure guard-path test — no daemon needed, so it runs even without docker.
	sb := &dockerSandbox{provider: NewDockerProvider(), name: "x", workdir: "/w"}
	if _, err := sb.Exec(context.Background(), nil, ExecOptions{}); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

// TestDockerRunArgs_RuntimeKnob asserts the --runtime flag is emitted only when
// Spec.Runtime is set — the OCI-runtime knob wiring — with no daemon needed.
func TestDockerRunArgs_RuntimeKnob(t *testing.T) {
	withRT := dockerRunArgs("box", DefaultWorkdir, Spec{Image: "alpine", Runtime: "runsc"})
	if !argvContains(withRT, "--runtime=runsc") {
		t.Fatalf("expected --runtime=runsc in argv, got %v", withRT)
	}
	noRT := dockerRunArgs("box", DefaultWorkdir, Spec{Image: "alpine"})
	for _, a := range noRT {
		if strings.HasPrefix(a, "--runtime") {
			t.Fatalf("expected no --runtime flag when Runtime empty, got %v", noRT)
		}
	}
	// Tail is `<image> sleep infinity` (docker keeps the command form).
	if j := strings.Join(noRT, " "); !strings.HasSuffix(j, "alpine sleep infinity") {
		t.Fatalf("expected `alpine sleep infinity` tail, got %v", noRT)
	}
}

// TestDockerRunArgs_HardeningDefaults proves the secure-by-default isolation and
// resource flags are emitted for a zero-valued Spec (the case the task runner
// hits) — the core of this security fix. No daemon needed.
func TestDockerRunArgs_HardeningDefaults(t *testing.T) {
	argv := dockerRunArgs("box", DefaultWorkdir, Spec{Image: "alpine"})

	// Network isolation: none by default (no egress, no reach to rqlite/Qdrant).
	if !argvContains(argv, "--network=none") {
		t.Errorf("expected --network=none by default, got %v", argv)
	}
	// Capability + privilege-escalation lockdown.
	if !argvContains(argv, "--cap-drop=ALL") {
		t.Errorf("expected --cap-drop=ALL by default, got %v", argv)
	}
	if !argvContains(argv, "--security-opt=no-new-privileges") {
		t.Errorf("expected --security-opt=no-new-privileges by default, got %v", argv)
	}
	// Read-only rootfs + writable tmpfs scratch.
	if !argvContains(argv, "--read-only") {
		t.Errorf("expected --read-only by default, got %v", argv)
	}
	if !argvPair(argv, "--tmpfs", scratchTmpfs) {
		t.Errorf("expected --tmpfs %s by default, got %v", scratchTmpfs, argv)
	}
	// Resource caps: fork-bomb, OOM, CPU.
	if !argvPair(argv, "--pids-limit", strconv.Itoa(DefaultPidsLimit)) {
		t.Errorf("expected --pids-limit %d by default, got %v", DefaultPidsLimit, argv)
	}
	if !argvPair(argv, "--memory", DefaultMemory) {
		t.Errorf("expected --memory %s by default, got %v", DefaultMemory, argv)
	}
	if !argvPair(argv, "--cpus", DefaultCPUs) {
		t.Errorf("expected --cpus %s by default, got %v", DefaultCPUs, argv)
	}
	// The idle command tail is preserved after all the hardening flags.
	if j := strings.Join(argv, " "); !strings.HasSuffix(j, "alpine sleep infinity") {
		t.Errorf("expected `alpine sleep infinity` tail, got %v", argv)
	}
}

// TestDockerRunArgs_HardeningOverridable proves every hardening default is a
// widen-when-needed knob: an explicit Spec value overrides the secure default.
func TestDockerRunArgs_HardeningOverridable(t *testing.T) {
	argv := dockerRunArgs("box", DefaultWorkdir, Spec{
		Image:              "alpine",
		Network:            "bridge",
		PidsLimit:          4096,
		Memory:             "2g",
		CPUs:               "4",
		Writable:           true,
		CapAdd:             []string{"NET_BIND_SERVICE"},
		AllowNewPrivileges: true,
	})

	// Widened network, resources.
	if !argvContains(argv, "--network=bridge") {
		t.Errorf("expected --network=bridge override, got %v", argv)
	}
	if argvContains(argv, "--network=none") {
		t.Errorf("secure default --network=none should be gone when overridden, got %v", argv)
	}
	if !argvPair(argv, "--pids-limit", "4096") {
		t.Errorf("expected --pids-limit 4096 override, got %v", argv)
	}
	if !argvPair(argv, "--memory", "2g") {
		t.Errorf("expected --memory 2g override, got %v", argv)
	}
	if !argvPair(argv, "--cpus", "4") {
		t.Errorf("expected --cpus 4 override, got %v", argv)
	}
	// Writable=true drops --read-only (and its tmpfs scratch).
	if argvContains(argv, "--read-only") {
		t.Errorf("Writable=true should drop --read-only, got %v", argv)
	}
	// cap-drop=ALL still present, with the requested cap added back.
	if !argvContains(argv, "--cap-drop=ALL") {
		t.Errorf("cap-drop=ALL must remain even with CapAdd, got %v", argv)
	}
	if !argvContains(argv, "--cap-add=NET_BIND_SERVICE") {
		t.Errorf("expected --cap-add=NET_BIND_SERVICE, got %v", argv)
	}
	// AllowNewPrivileges=true drops the no-new-privileges lock.
	if argvContains(argv, "--security-opt=no-new-privileges") {
		t.Errorf("AllowNewPrivileges=true should drop no-new-privileges, got %v", argv)
	}
}

// TestDockerRunArgs_PidsUnlimitedPassthrough proves a negative PidsLimit is a
// deliberate "unlimited" passthrough, distinct from 0 (apply default).
func TestDockerRunArgs_PidsUnlimitedPassthrough(t *testing.T) {
	argv := dockerRunArgs("box", DefaultWorkdir, Spec{Image: "alpine", PidsLimit: -1})
	if !argvPair(argv, "--pids-limit", "-1") {
		t.Errorf("expected --pids-limit -1 passthrough, got %v", argv)
	}
}

// TestHardeningFlags_ReadOnlyKeepsWorktreeWritable documents the read-only vs.
// writable-workspace interaction: --read-only locks the rootfs but a rw bind
// mount (the worktree) is emitted WITHOUT :ro, so the task keeps a writable
// workspace. This is the compromise that lets secure-by-default coexist with a
// task that must write its worktree.
func TestHardeningFlags_ReadOnlyKeepsWorktreeWritable(t *testing.T) {
	argv := dockerRunArgs("box", DefaultWorkdir, Spec{
		Image:      "alpine",
		BindMounts: []Mount{{Source: "/h/wt", Target: DefaultWorkdir}},
	})
	if !argvContains(argv, "--read-only") {
		t.Fatalf("expected --read-only, got %v", argv)
	}
	// The worktree mount is present and NOT read-only (no :ro suffix).
	if !argvContains(argv, "/h/wt:"+DefaultWorkdir) {
		t.Fatalf("expected rw worktree bind mount, got %v", argv)
	}
	if argvContains(argv, "/h/wt:"+DefaultWorkdir+":ro") {
		t.Fatalf("worktree mount must stay writable under --read-only, got %v", argv)
	}
}

// TestEnvFlags_Sorted asserts env flags render in deterministic sorted order.
func TestEnvFlags_Sorted(t *testing.T) {
	got := envFlags(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []string{"-e", "A=1", "-e", "B=2", "-e", "C=3"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("envFlags = %v, want %v", got, want)
	}
	if envFlags(nil) != nil {
		t.Fatal("envFlags(nil) should be nil")
	}
}

func TestCurrentUserSpec(t *testing.T) {
	if got := CurrentUserSpec(1000, 1000); got != "1000:1000" {
		t.Fatalf("CurrentUserSpec = %q", got)
	}
	if got := CurrentUserSpec(-1, 0); got != "" {
		t.Fatalf("CurrentUserSpec(-1,0) = %q, want empty", got)
	}
}
