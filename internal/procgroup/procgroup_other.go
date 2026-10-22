//go:build !unix

package procgroup

import "os/exec"

// Isolate is a no-op where Unix process groups do not exist.
func Isolate(*exec.Cmd) {}

// Kill kills the process alone where Unix process groups do not exist.
func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return errNotStarted
	}
	return cmd.Process.Kill()
}
