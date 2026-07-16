package plugintest_test

import (
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/plugintest"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// TestRefVectorBackend runs the full VectorBackend conformance suite against the
// in-memory reference backend, proving both the suite and the sealed decorator
// (semantic.NewVectorBackendStore) hold — including the central isolation seal.
func TestRefVectorBackend(t *testing.T) {
	plugintest.RunVectorBackendSuite(t, func() semantic.VectorBackend {
		return plugintest.NewRefVectorBackend()
	})
}

// TestRefDocBackend runs the full DocBackend conformance suite against the
// in-memory reference backend.
func TestRefDocBackend(t *testing.T) {
	plugintest.RunDocBackendSuite(t, func() facts.DocBackend {
		return plugintest.NewRefDocBackend()
	})
}
