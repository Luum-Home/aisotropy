package taskagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// fakeSandbox is a non-docker sandbox.Sandbox that records the argv/opts each Exec
// receives and returns canned output, so the sandboxExecutor's tool→argv mapping
// can be asserted without a container.
type fakeSandbox struct {
	calls    [][]string
	opts     []sandbox.ExecOptions
	stdout   string
	stderr   string
	exitCode int
	execErr  error
}

func (f *fakeSandbox) Exec(_ context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	f.calls = append(f.calls, argv)
	f.opts = append(f.opts, opts)
	if f.execErr != nil {
		return sandbox.ExecResult{}, f.execErr
	}
	return sandbox.ExecResult{Stdout: f.stdout, Stderr: f.stderr, ExitCode: f.exitCode}, nil
}

func (f *fakeSandbox) CopyIn(context.Context, string, string) error  { return nil }
func (f *fakeSandbox) CopyOut(context.Context, string, string) error { return nil }
func (f *fakeSandbox) Workdir() string                               { return sandbox.DefaultWorkdir }
func (f *fakeSandbox) Close(context.Context) error                   { return nil }

func TestSandboxExecutor_RunShellMapping(t *testing.T) {
	fs := &fakeSandbox{stdout: "file1\nfile2\n"}
	e := &sandboxExecutor{sb: fs}

	res, err := e.Execute(context.Background(), ToolRunShell, map[string]string{"cmd": "ls -la"})
	if err != nil {
		t.Fatalf("run_shell: %v", err)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("want 1 exec, got %d", len(fs.calls))
	}
	if got, want := fs.calls[0], []string{"sh", "-c", "ls -la"}; !equalArgv(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if res != "file1\nfile2\n" {
		t.Errorf("result = %q, want the stdout", res)
	}
}

func TestSandboxExecutor_RunShellStreamsOnLine(t *testing.T) {
	fs := &fakeSandbox{}
	var lines []string
	e := &sandboxExecutor{sb: fs, onLine: func(l string) { lines = append(lines, l) }}
	if _, err := e.Execute(context.Background(), ToolRunShell, map[string]string{"cmd": "echo hi"}); err != nil {
		t.Fatalf("run_shell: %v", err)
	}
	// The executor must thread its onLine hook into ExecOptions so live streaming
	// reaches the SSE fan-out.
	if fs.opts[0].OnLine == nil {
		t.Fatal("run_shell did not pass OnLine into ExecOptions")
	}
	_ = lines
}

func TestSandboxExecutor_WriteFileMapping(t *testing.T) {
	fs := &fakeSandbox{}
	e := &sandboxExecutor{sb: fs}

	content := "line1\nweird ' quote \" and $VAR\n"
	res, err := e.Execute(context.Background(), ToolWriteFile, map[string]string{"path": "src/a.txt", "content": content})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("want 1 exec, got %d", len(fs.calls))
	}
	argv := fs.calls[0]
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("argv = %v, want [sh -c <script>]", argv)
	}
	script := argv[2]
	// The content must NOT appear raw in the script (it is base64-transported), the
	// single-quoted path must appear, and the decode must go through base64 -d.
	if strings.Contains(script, content) {
		t.Errorf("script leaked raw content (should be base64): %q", script)
	}
	if !strings.Contains(script, "base64 -d") {
		t.Errorf("script does not decode via base64 -d: %q", script)
	}
	if !strings.Contains(script, "'src/a.txt'") {
		t.Errorf("script does not carry the single-quoted path: %q", script)
	}
	if !strings.Contains(res, "wrote") || !strings.Contains(res, "src/a.txt") {
		t.Errorf("result = %q, want a wrote-confirmation", res)
	}
}

func TestSandboxExecutor_ReadFileMapping(t *testing.T) {
	fs := &fakeSandbox{stdout: "hello world"}
	e := &sandboxExecutor{sb: fs}
	res, err := e.Execute(context.Background(), ToolReadFile, map[string]string{"path": "notes.md"})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if got, want := fs.calls[0], []string{"cat", "notes.md"}; !equalArgv(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if res != "hello world" {
		t.Errorf("result = %q, want the stdout", res)
	}
}

func TestSandboxExecutor_ReadFileMissingIsResultNotError(t *testing.T) {
	fs := &fakeSandbox{stderr: "cat: notes.md: No such file", exitCode: 1}
	e := &sandboxExecutor{sb: fs}
	res, err := e.Execute(context.Background(), ToolReadFile, map[string]string{"path": "notes.md"})
	if err != nil {
		t.Fatalf("read_file non-zero exit should not be a Go error: %v", err)
	}
	if !strings.Contains(res, "No such file") {
		t.Errorf("result = %q, want the stderr folded in", res)
	}
}

func TestSandboxExecutor_UnknownToolErrors(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{}}
	if _, err := e.Execute(context.Background(), "definitely_not_a_tool", nil); err == nil {
		t.Fatal("unknown tool should error")
	}
}

// TestSandboxExecutor_GitCommitHostSide verifies git_commit stages+commits the
// host-visible worktree (git_commit runs host-side by design, not in the
// container). Skips if git is unavailable.
func TestSandboxExecutor_GitCommitHostSide(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)
	// Write a file directly in the repo (stands in for a container edit into the
	// bind-mounted worktree), then commit via the executor.
	writeFileHost(t, repo, "note.txt", "hello")

	e := &sandboxExecutor{sb: &fakeSandbox{}, worktreeDir: repo}
	res, err := e.Execute(context.Background(), ToolGitCommit, map[string]string{"message": "add note"})
	if err != nil {
		t.Fatalf("git_commit: %v", err)
	}
	if !strings.Contains(res, "committed") {
		t.Fatalf("result = %q, want a commit confirmation", res)
	}
	// The commit must be real: rev-list HEAD shows exactly two commits (initial +
	// ours) and the tree contains note.txt.
	out := gitOut(t, repo, "log", "--oneline")
	if strings.Count(strings.TrimSpace(out), "\n") != 1 { // 2 lines => 1 newline
		t.Errorf("log = %q, want 2 commits", out)
	}
	if got := gitOut(t, repo, "show", "HEAD:note.txt"); strings.TrimSpace(got) != "hello" {
		t.Errorf("committed content = %q, want hello", got)
	}
}

func TestSandboxExecutor_GitCommitNothingIsResultNotError(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)
	e := &sandboxExecutor{sb: &fakeSandbox{}, worktreeDir: repo}
	res, err := e.Execute(context.Background(), ToolGitCommit, map[string]string{"message": "noop"})
	if err != nil {
		t.Fatalf("git_commit empty should not be a Go error: %v", err)
	}
	if !strings.Contains(res, "nothing") {
		t.Errorf("result = %q, want a nothing-to-commit note", res)
	}
}

func TestHandsRegistry_ScopeGating(t *testing.T) {
	reg := handsRegistry()
	// A caller granted only read access sees exactly read_file.
	permitted := reg.Permitted([]string{ScopeReadFile})
	if len(permitted) != 1 || permitted[0].Name != ToolReadFile {
		t.Fatalf("read-only scope permitted = %v, want [read_file]", permitted)
	}
	// The full set grants all four hands, each carrying an advertised schema.
	all := reg.Permitted(HandScopes())
	if len(all) != 4 {
		t.Fatalf("full scopes permitted %d tools, want 4", len(all))
	}
	for _, tl := range all {
		if len(tl.Parameters) == 0 {
			t.Errorf("tool %q has no advertised schema", tl.Name)
		}
	}
}

func TestDecodeStringArgs(t *testing.T) {
	got := decodeStringArgs(`{"path":"a.txt","n":3,"ok":true}`)
	if got["path"] != "a.txt" || got["n"] != "3" || got["ok"] != "true" {
		t.Errorf("decodeStringArgs = %v", got)
	}
	if len(decodeStringArgs("")) != 0 || len(decodeStringArgs("null")) != 0 {
		t.Error("empty/null should decode to empty map")
	}
}

// --- helpers ---

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func gitReady() bool { return exec.Command("git", "--version").Run() == nil }

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	writeFileHost(t, dir, "README.md", "seed")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "initial")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFileHost(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
