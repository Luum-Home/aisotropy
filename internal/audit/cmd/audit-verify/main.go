// Command audit-verify re-walks a hash-chained access-audit log (ADR-0056) and
// reports whether the chain is intact. It is an operator/auditor tool: point it at
// the AISOTROPY_AUDIT_LOG file produced with AISOTROPY_AUDIT_HASHCHAIN enabled.
//
// Exit codes:
//
//	0  chain verified intact
//	1  chain broken (a record was edited, removed, or reordered) — the 0-based
//	   index of the first bad record is printed
//	2  usage or I/O error (could not open/read the file)
//
// Honest limit (see ADR-0056): a verifier holding only the file cannot detect tail
// truncation — a valid prefix of a longer chain still verifies. Detecting that
// needs an external anchor of the head digest.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable body of main(): it parses -path out of args, verifies
// the hash-chained audit log at that path, writes its report to stdout/
// stderr, and returns the process exit code main() should use (0/1/2, see
// the package doc). It never calls os.Exit itself, so every path — usage
// error, an unreadable file, a broken chain, an unparseable record, and a
// clean verification — is directly testable without a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("path", "", "path to the hash-chained audit log (JSONL) to verify")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(stderr, "usage: audit-verify -path <audit-log.jsonl>")
		return 2
	}

	ok, brokenAt, err := audit.VerifyFile(*path)
	if err != nil {
		fmt.Fprintf(stderr, "audit-verify: %v\n", err)
		if brokenAt >= 0 && !ok {
			// A parse failure carries the offending index.
			fmt.Fprintf(stderr, "audit-verify: first unparseable record at index %d\n", brokenAt)
			return 1
		}
		return 2
	}
	if !ok {
		fmt.Fprintf(stdout, "BROKEN: audit chain %q fails verification at record index %d (edited, removed, or reordered)\n", *path, brokenAt)
		return 1
	}
	fmt.Fprintf(stdout, "OK: audit chain %q verified intact\n", *path)
	return 0
}
