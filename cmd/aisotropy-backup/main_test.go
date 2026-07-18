package main

import (
	"os"
	"strings"
	"testing"
)

// TestRunHelp covers every alias that prints usage without dispatching to a
// tier/action handler: no args, -h, --help, help.
func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-h"},
		{"--help"},
		{"help"},
	} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
}

// TestRunTooFewArgs covers the "expected <tier> <action>" guard for a single
// bare (non-help) argument.
func TestRunTooFewArgs(t *testing.T) {
	err := run([]string{"badger"})
	if err == nil {
		t.Fatal("run([\"badger\"]) = nil, want error")
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Errorf("run([\"badger\"]) error = %q, want it to mention the expected form", err.Error())
	}
}

// TestRunUnknownCommand covers the fallthrough "unknown command" branch for
// an unrecognized tier and for a recognized tier with an unrecognized action.
func TestRunUnknownCommand(t *testing.T) {
	cases := [][]string{
		{"nope", "backup"},
		{"badger", "nope"},
		{"sqlite", "nope"},
		{"qdrant", "nope"},
	}
	for _, args := range cases {
		err := run(args)
		if err == nil {
			t.Fatalf("run(%v) = nil, want error", args)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("run(%v) error = %q, want it to mention unknown command", args, err.Error())
		}
	}
}

// TestRunDispatchesToTierActions exercises every valid <tier> <action> pair so
// the switch's call-out lines are covered. Each call is given deliberately
// incomplete flags so the underlying handler returns fast via its own flag
// validation — that handler's success/error paths are covered independently
// in badger_test.go, sqlite_test.go, and qdrant_test.go.
func TestRunDispatchesToTierActions(t *testing.T) {
	t.Setenv("AISOTROPY_DATA_DIR", "")
	t.Setenv("QDRANT_HOST", "")
	t.Setenv("QDRANT_COLLECTION", "")

	cases := [][]string{
		{"badger", "backup"},
		{"badger", "restore"},
		{"sqlite", "backup"},
		{"sqlite", "restore"},
		{"qdrant", "snapshot"},
		{"qdrant", "restore"},
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Errorf("run(%v) = nil, want a flag-validation error (no required flags supplied)", args)
		}
	}
}

// TestMainSuccessPath drives main() end-to-end for the help path, where run()
// returns nil and osExit must never be invoked.
func TestMainSuccessPath(t *testing.T) {
	origArgs := os.Args
	origExit := osExit
	defer func() {
		os.Args = origArgs
		osExit = origExit
	}()

	os.Args = []string{"aisotropy-backup", "-h"}
	exitCalled := false
	osExit = func(code int) { exitCalled = true }

	main()

	if exitCalled {
		t.Error("main() called osExit on the help path, want no exit")
	}
}

// TestMainFailurePath drives main() end-to-end for a dispatch error, asserting
// osExit(1) is invoked instead of the process actually terminating.
func TestMainFailurePath(t *testing.T) {
	origArgs := os.Args
	origExit := osExit
	defer func() {
		os.Args = origArgs
		osExit = origExit
	}()

	os.Args = []string{"aisotropy-backup", "nope", "nope"}
	var gotCode int
	exitCalled := false
	osExit = func(code int) {
		exitCalled = true
		gotCode = code
	}

	main()

	if !exitCalled {
		t.Fatal("main() did not call osExit on an unknown command, want osExit(1)")
	}
	if gotCode != 1 {
		t.Errorf("osExit code = %d, want 1", gotCode)
	}
}
