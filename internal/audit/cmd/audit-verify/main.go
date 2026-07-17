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
	"os"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

func main() {
	path := flag.String("path", "", "path to the hash-chained audit log (JSONL) to verify")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: audit-verify -path <audit-log.jsonl>")
		os.Exit(2)
	}

	ok, brokenAt, err := audit.VerifyFile(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-verify: %v\n", err)
		if brokenAt >= 0 && !ok {
			// A parse failure carries the offending index.
			fmt.Fprintf(os.Stderr, "audit-verify: first unparseable record at index %d\n", brokenAt)
			os.Exit(1)
		}
		os.Exit(2)
	}
	if !ok {
		fmt.Printf("BROKEN: audit chain %q fails verification at record index %d (edited, removed, or reordered)\n", *path, brokenAt)
		os.Exit(1)
	}
	fmt.Printf("OK: audit chain %q verified intact\n", *path)
}
