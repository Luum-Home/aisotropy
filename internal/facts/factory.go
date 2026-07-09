package facts

import (
	"os"
	"path/filepath"
)

// OpenFromEnv builds the facts Store from the environment, mirroring the
// working-memory tier's config precedence (see cmd/aisotropyd/main.go):
//
//   - RQLITE_URL set         -> networked RqliteStore (ADR-0004 production path)
//   - AISOTROPY_FACTS_DB set -> embedded SQLiteStore at that file path
//   - AISOTROPY_DATA_DIR set -> embedded SQLiteStore at <dir>/facts.db
//   - none                   -> ephemeral in-memory SQLiteStore (zero-config)
//
// The zero-config default keeps the binary runnable with no external services,
// consistent with the walking-skeleton posture of the working-memory tier.
func OpenFromEnv() (Store, error) {
	if url := os.Getenv("RQLITE_URL"); url != "" {
		return OpenRqlite(url)
	}
	if path := os.Getenv("AISOTROPY_FACTS_DB"); path != "" {
		return OpenSQLite(path)
	}
	if dir := os.Getenv("AISOTROPY_DATA_DIR"); dir != "" {
		return OpenSQLite(filepath.Join(dir, "facts.db"))
	}
	return OpenSQLite(":memory:")
}
