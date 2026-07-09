// Package sleepcycle is the Go side of the offline batch consolidation
// (the "sleep cycle", docs/components/datafusion-sleep-cycle.md). Per ADR-0005
// the heavy analytics run in a SEPARATE Rust worker (Apache DataFusion) that Go
// invokes as a subprocess with an explicit, tested I/O contract — never CGO,
// never in-process — so the Go core stays a static binary and a worker panic is
// isolated as a non-zero exit code, retried with backoff.
//
// This package owns that contract: it materializes the input the worker reads
// (newline-delimited JSON of facts), invokes the binary, parses its one-line
// JSON summary, and turns exit codes into Go errors with retry/backoff and an
// OTel span per run.
package sleepcycle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Record is one row exported to the worker. The field names ARE the schema the
// DataFusion query reads (crates/sleep-cycle) — keep them in lockstep. Times are
// unix milliseconds (matching internal/facts).
type Record struct {
	ID         string  `json:"id"`
	TenantID   string  `json:"tenant_id"`
	UserID     string  `json:"user_id"`
	Content    string  `json:"content"`
	Importance float64 `json:"importance"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

// WriteNDJSON writes records as newline-delimited JSON to path — the exact
// format the worker reads via DataFusion's NdJson reader. Materializing to disk
// (rather than sharing memory) is deliberate: it gives the batch job
// checkpointing, reproducibility, and debuggability (ADR-0005 consequences).
func WriteNDJSON(path string, records []Record) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("sleepcycle: create input %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i, r := range records {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("sleepcycle: encode record %d: %w", i, err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("sleepcycle: flush input: %w", err)
	}
	return nil
}
