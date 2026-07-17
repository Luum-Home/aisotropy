package main

import "flag"

// newFlagSet builds a ContinueOnError FlagSet so a parse error (or -h) returns
// to the dispatcher as an error/`flag.ErrHelp` instead of calling os.Exit,
// which keeps the CLI testable.
func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}
