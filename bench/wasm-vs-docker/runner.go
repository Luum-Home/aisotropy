package main

import (
	"os"
	"os/exec"
)

// cmdSpec is one external command invocation — everything the guest builders
// (go/tinygo/rustc) and the docker/toolchain/ps probes need to shell out.
// Bundling Name/Args/Dir/Env into a value type (rather than driving
// *exec.Cmd directly) is what makes the call fakeable in tests: a fakeRunner
// matches on the spec instead of needing a real process.
type cmdSpec struct {
	Name string
	Args []string
	Dir  string   // optional working directory; empty means the caller's cwd.
	Env  []string // optional extra env vars, appended to os.Environ() by execRunner.
}

// cmdRunner abstracts external process execution so the guest builders and the
// docker/toolchain/ps probes are testable without a real go/tinygo/rustc/
// docker/ps on PATH. execRunner is the real, os/exec-backed implementation
// main wires up; tests substitute a fakeRunner (see runner_test.go).
type cmdRunner interface {
	// LookPath resolves file to an absolute path, exactly like exec.LookPath.
	LookPath(file string) (string, error)
	// Output runs spec and returns its captured stdout, like (*exec.Cmd).Output.
	Output(spec cmdSpec) ([]byte, error)
	// CombinedOutput runs spec and returns merged stdout+stderr, like
	// (*exec.Cmd).CombinedOutput.
	CombinedOutput(spec cmdSpec) ([]byte, error)
	// Run runs spec and reports only whether it succeeded, like (*exec.Cmd).Run.
	Run(spec cmdSpec) error
}

// execRunner is the real cmdRunner, backed by os/exec. It carries no state.
type execRunner struct{}

// LookPath implements cmdRunner.
func (execRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

func (execRunner) build(spec cmdSpec) *exec.Cmd {
	cmd := exec.Command(spec.Name, spec.Args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	return cmd
}

// Output implements cmdRunner.
func (r execRunner) Output(spec cmdSpec) ([]byte, error) { return r.build(spec).Output() }

// CombinedOutput implements cmdRunner.
func (r execRunner) CombinedOutput(spec cmdSpec) ([]byte, error) {
	return r.build(spec).CombinedOutput()
}

// Run implements cmdRunner.
func (r execRunner) Run(spec cmdSpec) error { return r.build(spec).Run() }
