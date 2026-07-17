//go:build !keybroker

package main

import "io"

// runOptionalCommand is the default-build stub. The key-brokering `task`
// subcommand is compiled only under -tags keybroker (it needs a container
// runtime at call time), so the default binary owns no optional commands and
// reports every unrecognized command as unknown.
func runOptionalCommand(cmd string, args []string, stdout, stderr io.Writer) (code int, handled bool) {
	return 0, false
}

// optionalUsage is a no-op in the default build: there are no build-tagged
// subcommands to advertise.
func optionalUsage(w io.Writer) {}
