package main

import "strings"

// reorderFlagsFirst moves flag tokens ahead of positional tokens so the stdlib
// flag parser (which halts at the first non-flag argument) accepts flags on
// EITHER side of a positional argument. It is shared by the `new` subcommand
// (always built) and the `task` subcommand (built only under -tags keybroker),
// so it lives in this untagged file rather than in either command's file — the
// default build needs it for `new` even when `task` is not compiled in.
//
// Every flag those subcommands define takes a value, so a bare "--name" token
// consumes the following token as its value; the "--name=value" form is
// self-contained. Unknown flags are passed through untouched so fs.Parse still
// reports them as errors.
func reorderFlagsFirst(args []string) []string {
	valueFlags := map[string]bool{
		"-prompt": true, "--prompt": true,
		"-provider": true, "--provider": true,
		"-image": true, "--image": true,
		"-max-steps": true, "--max-steps": true,
		// The `new` subcommand's --module flag takes a value too, so it must be
		// listed here to be pulled along with its argument when it appears before
		// the positional <dir>.
		"-module": true, "--module": true,
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Everything after "--" is positional by convention.
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// A bare known value-flag (no "=") pulls in the next token as its value.
			if !strings.Contains(a, "=") && valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}
