package plugintest

import (
	"testing"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// TestBadgerWorkingBackendConformance runs the SAME suite against the real
// embedded driver.
//
// This is the run that makes the interface evidenced rather than plausible. The
// in-memory reference backend has free transactions, free ordering and no key
// encoding, so it can satisfy a contract a persistent store cannot: it proves
// the suite runs, not that the contract is implementable. Badger has real
// lexicographic iteration (which is why the sequence is zero-padded), real
// transactions, and real prefix scans.
func TestBadgerWorkingBackendConformance(t *testing.T) {
	RunWorkingBackendSuite(t, func() memory.WorkingBackend {
		b, err := memory.OpenBadgerWorkingBackend()
		if err != nil {
			t.Fatalf("OpenBadgerWorkingBackend: %v", err)
		}
		return b
	})
}
