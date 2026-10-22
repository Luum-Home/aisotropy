//go:build unix

package procgroup

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startForking starts a shell that backgrounds `sleep <secs>` (a grandchild
// that inherits stdout) and returns once the shell has reported the fork, so
// the grandchild is guaranteed to exist when the caller kills.
func startForking(t *testing.T, secs string, isolate bool) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep "+secs+" & echo ready; wait")
	if isolate {
		Isolate(cmd)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	if line, err := r.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("first line = %q, err = %v", line, err)
	}
	return cmd, r
}

// drain reads r to EOF in the background; the channel closes at EOF.
func drain(r io.Reader) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()
	return done
}

// within reports whether done closes within d.
func within(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestKillReachesGrandchildHoldingThePipe(t *testing.T) {
	cmd, r := startForking(t, "30", true)
	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// The grandchild sleeps 30s; EOF within 5s means it died with the group.
	if !within(drain(r), 5*time.Second) {
		t.Fatal("stdout still open: the grandchild survived the group kill")
	}
	_ = cmd.Wait()
}

// TestKillWithoutIsolateLeavesGrandchild is the control: the same fork killed
// as a single process leaves the grandchild holding stdout open. Without this
// the test above could pass for a reason other than the group kill.
func TestKillWithoutIsolateLeavesGrandchild(t *testing.T) {
	cmd, r := startForking(t, "2", false)
	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	drained := drain(r)
	if within(drained, 500*time.Millisecond) {
		t.Fatal("stdout closed at once: the control no longer has a grandchild holding it")
	}
	// Wait for the 2s grandchild to exit so nothing outlives the test.
	if !within(drained, 10*time.Second) {
		t.Fatal("grandchild never exited")
	}
	_ = cmd.Wait()
}

func TestKillNotStarted(t *testing.T) {
	if err := Kill(exec.Command("true")); !errors.Is(err, errNotStarted) {
		t.Fatalf("Kill on unstarted cmd = %v, want errNotStarted", err)
	}
}

func TestKillGoneGroupIsProcessDone(t *testing.T) {
	cmd := exec.Command("true")
	Isolate(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Once Wait has reaped the group's only member, the group no longer exists:
	// a late Kill (e.g. a watchdog racing a teardown) reports ErrProcessDone.
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill on a gone group = %v, want os.ErrProcessDone", err)
	}
}

func TestIsolateKeepsOtherAttributes(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: false, Noctty: true}
	Isolate(cmd)
	if !cmd.SysProcAttr.Setpgid || !cmd.SysProcAttr.Noctty {
		t.Fatalf("SysProcAttr = %+v, want Setpgid and the existing Noctty", cmd.SysProcAttr)
	}
}
