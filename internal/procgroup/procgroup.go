// Package procgroup starts a child process as the leader of its own process
// group so it can be stopped together with everything it spawned.
//
// Killing only the direct child is not enough when that child forks. A
// grandchild inherits the child's stdout/stderr pipes and keeps them open after
// its parent dies, so a reader blocked on those pipes — and exec.Cmd.Wait,
// which drains a non-*os.File Stdout/Stderr — hangs until the grandchild exits
// on its own. Common ways to end up with such a grandchild:
//
//   - `sh -c "sleep 5"` under dash (Debian/Ubuntu /bin/sh), which forks the
//     last command where bash would exec it;
//   - a shell-script wrapper around a worker binary (a loop, or any command
//     that is not the script's final exec);
//   - `go run`, which builds the program and then runs it as its own child.
//
// Isolate + Kill turn "kill the child" into "kill the child's process group",
// which reaches those grandchildren and closes the pipes promptly.
package procgroup

import "errors"

// errNotStarted is returned by Kill for a command whose process never started.
var errNotStarted = errors.New("procgroup: process not started")
