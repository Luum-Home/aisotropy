//go:build unix

package procgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Isolate makes cmd, once started, the leader of a new process group whose id
// is its pid. Call it before cmd.Start. Other SysProcAttr fields are kept.
func Isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
}

// Kill sends SIGKILL to cmd's whole process group when Isolate was applied
// before Start, and to the process alone otherwise. A group with no process
// left reports os.ErrProcessDone, like (*os.Process).Kill does for an exited
// process. It is safe to call from a goroutine other than the one in Wait: it
// only reads cmd.Process.Pid, which is fixed at Start.
//
// Call it before Wait reaps the leader: after that the group id is a free pid
// again. Every caller in this module kills first and waits second.
func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return errNotStarted
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return cmd.Process.Kill()
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
