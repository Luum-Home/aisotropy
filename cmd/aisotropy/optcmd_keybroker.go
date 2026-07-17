//go:build keybroker

package main

import "io"

// runOptionalCommand dispatches build-tagged subcommands present only in the
// keybroker build. Today that is `task` — the sandboxed key-brokered coding
// primitive (ADR-0036) that needs a container runtime at call time and is
// therefore compiled only under -tags keybroker. It returns handled=false for
// anything it does not own, so the caller falls through to its unknown-command
// error.
func runOptionalCommand(cmd string, args []string, stdout, stderr io.Writer) (code int, handled bool) {
	switch cmd {
	case "task":
		return runTask(args, stdout, stderr), true
	default:
		return 0, false
	}
}

// optionalUsage appends the build-tagged subcommands to the top-level usage in
// the keybroker build.
func optionalUsage(w io.Writer) {
	_, _ = io.WriteString(w,
		"  aisotropy task run <repo-dir>         Run one sandboxed agentic coding task (--prompt required)\n")
}
