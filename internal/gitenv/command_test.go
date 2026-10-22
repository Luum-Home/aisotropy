package gitenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = clean(os.Environ()) // snapshot/setup stays independent of Command.
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "add", "sentinel")
	fixtureGit(t, dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial")
	return dir
}

func repoSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	state := map[string]string{
		"HEAD": fixtureGit(t, dir, "rev-parse", "HEAD"),
		"tree": fixtureGit(t, dir, "rev-parse", "HEAD^{tree}"),
	}
	for _, path := range []string{".git/config", ".git/index", "sentinel"} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		state[path] = string(data)
	}
	return state
}

func poisonGit(t *testing.T, victim string) {
	t.Helper()
	for key, value := range map[string]string{
		"GIT_DIR":               filepath.Join(victim, ".git"),
		"GIT_COMMON_DIR":        filepath.Join(victim, ".git"),
		"GIT_WORK_TREE":         victim,
		"GIT_INDEX_FILE":        filepath.Join(victim, ".git/index"),
		"GIT_CONFIG_COUNT":      "1",
		"GIT_CONFIG_KEY_0":      "user.name",
		"GIT_CONFIG_VALUE_0":    "injected",
		"GIT_CONFIG_PARAMETERS": "'user.email=poison@example.invalid'",
	} {
		t.Setenv(key, value)
	}
}

func TestCommandIgnoresInheritedGitEnvironment(t *testing.T) {
	victim := fixtureRepo(t)
	before := repoSnapshot(t, victim)
	poisonGit(t, victim)
	t.Cleanup(func() {
		if !reflect.DeepEqual(before, repoSnapshot(t, victim)) {
			t.Error("inherited Git environment changed victim config, HEAD, index or tree")
		}
	})
	target := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.name", "target"},
		{"config", "user.email", "target@example.invalid"},
		{"commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := Command(context.Background(), "git", append([]string{"-C", target}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("target git %v: %v\n%s", args, err, out)
		}
	}
	if got := fixtureGit(t, target, "config", "user.name"); got != "target\n" {
		t.Fatalf("target identity = %q", got)
	}
	if os.Getenv("GIT_DIR") != filepath.Join(victim, ".git") {
		t.Fatal("Command changed the parent environment")
	}
}

func TestInheritedGitEnvironmentNegativeControl(t *testing.T) {
	victim := fixtureRepo(t)
	before := repoSnapshot(t, victim)
	poisonGit(t, victim)
	// Deliberately retain the old vulnerable operation. Both paths are fixtures;
	// a zero exit plus changed victim proves that -C is not an isolation boundary.
	cmd := exec.Command("git", "-C", t.TempDir(), "config", "user.name", "leaked")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("negative control did not exercise the vulnerable write: %v\n%s", err, out)
	}
	if reflect.DeepEqual(before, repoSnapshot(t, victim)) {
		t.Fatal("negative control failed to detect the fixture victim write")
	}
}
