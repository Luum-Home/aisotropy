package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// readChainLines returns the non-empty lines of a chain file as raw byte slices.
func readChainLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chain file: %v", err)
	}
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), b...))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan chain file: %v", err)
	}
	return out
}

// writeChainLines writes lines back to path joined by newlines (each terminated).
func writeChainLines(t *testing.T, path string, lines [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write chain file: %v", err)
	}
}

func writeSampleChain(t *testing.T, path string, n int) *HashChainedSink {
	t.Helper()
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	for i := 0; i < n; i++ {
		e := sampleEvent()
		e.UserID = "user-" + string(rune('a'+i%26))
		e.Status = 200 + i
		sink.Record(e)
	}
	return sink
}

// (a) A well-formed chain verifies OK.
func TestHashChain_WellFormedVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := writeSampleChain(t, path, 5)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if !ok || brokenAt != -1 {
		t.Fatalf("want intact chain (ok=true, brokenAt=-1), got ok=%v brokenAt=%d", ok, brokenAt)
	}

	// Structural check: record 0 chains from genesis; each record links to the prior.
	lines := readChainLines(t, path)
	if len(lines) != 5 {
		t.Fatalf("want 5 records, got %d", len(lines))
	}
	var prev string = GenesisHash
	for i, l := range lines {
		var rec chainedRecord
		if err := json.Unmarshal(l, &rec); err != nil {
			t.Fatalf("unmarshal record %d: %v", i, err)
		}
		if rec.PrevHash != prev {
			t.Errorf("record %d prev_hash = %q, want %q", i, rec.PrevHash, prev)
		}
		prev = rec.Hash
	}
}

// (b1) Editing any PAST record's content breaks verification at that index.
func TestHashChain_TamperedContentBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := writeSampleChain(t, path, 5)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	// Tamper with record index 2's Event content, but KEEP its stored hash intact
	// (an attacker who edits a field but does not recompute the digest). The
	// recomputed hash over the edited event no longer matches the stored hash.
	var rec chainedRecord
	if err := json.Unmarshal(lines[2], &rec); err != nil {
		t.Fatalf("unmarshal record 2: %v", err)
	}
	rec.TenantID = "attacker-swapped-tenant"
	forged, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal forged record: %v", err)
	}
	lines[2] = forged
	writeChainLines(t, path, lines)

	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if ok {
		t.Fatal("want broken chain after content tamper, got ok=true")
	}
	if brokenAt != 2 {
		t.Fatalf("want break at index 2, got %d", brokenAt)
	}
}

// (b2) Removing a record breaks the linkage of the following record.
func TestHashChain_RemovedRecordBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := writeSampleChain(t, path, 5)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	// Remove record index 2. Record 3 (now at index 2) still carries prev_hash =
	// hash(old record 2), which no longer equals the hash of record 1 -> break at 2.
	lines = append(lines[:2], lines[3:]...)
	writeChainLines(t, path, lines)

	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if ok {
		t.Fatal("want broken chain after record removal, got ok=true")
	}
	if brokenAt != 2 {
		t.Fatalf("want break at index 2 after removing record 2, got %d", brokenAt)
	}
}

// (b3) Removing the FIRST record is detected via the genesis anchor.
func TestHashChain_RemovedGenesisRecordBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := writeSampleChain(t, path, 4)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	lines = lines[1:] // drop record 0
	writeChainLines(t, path, lines)

	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if ok || brokenAt != 0 {
		t.Fatalf("want break at index 0 (genesis anchor), got ok=%v brokenAt=%d", ok, brokenAt)
	}
}

// (b4) Reordering two records breaks verification.
func TestHashChain_ReorderBreaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := writeSampleChain(t, path, 5)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	lines[1], lines[2] = lines[2], lines[1] // swap records 1 and 2
	writeChainLines(t, path, lines)

	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if ok {
		t.Fatal("want broken chain after reorder, got ok=true")
	}
	if brokenAt != 1 {
		t.Fatalf("want break at index 1 after swap, got %d", brokenAt)
	}
}

// (c) Concurrency-safe append: concurrent Records still produce one intact chain
// with every event persisted.
func TestHashChain_ConcurrentRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}

	const goroutines = 32
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				e := sampleEvent()
				e.RequestID = "g" + string(rune('0'+g%10))
				sink.Record(e)
			}
		}(g)
	}
	wg.Wait()
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	if len(lines) != goroutines*perG {
		t.Fatalf("want %d records, got %d", goroutines*perG, len(lines))
	}
	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if !ok || brokenAt != -1 {
		t.Fatalf("concurrent chain not intact: ok=%v brokenAt=%d", ok, brokenAt)
	}
}

// Restart continuity: reopening the same path resumes the SAME chain, and the
// combined file verifies as one unbroken chain.
func TestHashChain_ResumesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	s1, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("open s1: %v", err)
	}
	s1.Record(sampleEvent())
	s1.Record(sampleEvent())
	if err := s1.Close(); err != nil {
		t.Fatalf("close s1: %v", err)
	}

	s2, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("reopen s2: %v", err)
	}
	s2.Record(sampleEvent())
	if err := s2.Close(); err != nil {
		t.Fatalf("close s2: %v", err)
	}

	lines := readChainLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("want 3 records after restart, got %d", len(lines))
	}
	ok, brokenAt, err := VerifyFile(path)
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if !ok || brokenAt != -1 {
		t.Fatalf("resumed chain not intact: ok=%v brokenAt=%d", ok, brokenAt)
	}
}

// Opening a corrupt existing chain fails closed rather than silently appending.
func TestHashChain_CorruptExistingFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, []byte("{not valid json}\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := NewHashChainedSink(path); err == nil {
		t.Fatal("want error opening a corrupt existing chain, got nil")
	}
}

// Privacy: a hash-chained record carries identity + metadata + the two chain
// digests ONLY — never a body, response, header, or secret-shaped field.
func TestHashChain_NoBodyOrSecretFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	sink.Record(sampleEvent())
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readChainLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("want 1 record, got %d", len(lines))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(lines[0], &m); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	allowed := map[string]bool{
		"timestamp": true, "tenant_id": true, "user_id": true,
		"method": true, "route": true, "status": true,
		"request_id": true, "latency_ms": true,
		"prev_hash": true, "hash": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("unexpected hash-chain field %q: records must carry identity + metadata + chain digests only", k)
		}
	}
	lower := strings.ToLower(string(lines[0]))
	for _, forbidden := range []string{"prompt", "response", "password", "secret", "authorization", "api_key", "apikey"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("serialized chained record contains forbidden token %q: %s", forbidden, lines[0])
		}
	}
}

// An empty stream verifies vacuously (no records to break).
func TestHashChain_EmptyVerifies(t *testing.T) {
	ok, brokenAt, err := VerifyChain(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("VerifyChain(empty): %v", err)
	}
	if !ok || brokenAt != -1 {
		t.Fatalf("want empty stream intact, got ok=%v brokenAt=%d", ok, brokenAt)
	}
}

// A custom (non-default) genesis verifies with VerifyChainFrom but fails the
// default-anchor VerifyChain at index 0.
func TestHashChain_CustomGenesisAnchor(t *testing.T) {
	const bootNonce = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef0"
	e := sampleEvent()
	canon, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	rec := chainedRecord{Event: e, PrevHash: bootNonce, Hash: chainHash(bootNonce, canon)}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}

	if ok, _, err := VerifyChainFrom(bytes.NewReader(line), bootNonce); err != nil || !ok {
		t.Fatalf("VerifyChainFrom(bootNonce) want ok, got ok=%v err=%v", ok, err)
	}
	if ok, brokenAt, err := VerifyChain(bytes.NewReader(line)); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	} else if ok || brokenAt != 0 {
		t.Fatalf("default anchor should reject custom genesis at index 0, got ok=%v brokenAt=%d", ok, brokenAt)
	}
}

// FromEnv wiring: AISOTROPY_AUDIT_HASHCHAIN selects HashChainedSink when a path is
// set, and fails closed when it is not.
func TestFromEnv_HashChainSelection(t *testing.T) {
	t.Run("hashchain + path selects HashChainedSink", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a.jsonl")
		t.Setenv(EnvDisable, "")
		t.Setenv(EnvLogPath, path)
		t.Setenv(EnvHashChain, "true")
		s, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		defer s.Close()
		if _, ok := s.(*HashChainedSink); !ok {
			t.Fatalf("want *HashChainedSink, got %T", s)
		}
	})

	t.Run("hashchain without path fails closed", func(t *testing.T) {
		t.Setenv(EnvDisable, "")
		t.Setenv(EnvLogPath, "")
		t.Setenv(EnvHashChain, "1")
		if _, err := FromEnv(); err == nil {
			t.Fatal("want error when hashchain is set without a durable path, got nil")
		}
	})

	t.Run("disable still wins over hashchain", func(t *testing.T) {
		t.Setenv(EnvLogPath, filepath.Join(t.TempDir(), "a.jsonl"))
		t.Setenv(EnvHashChain, "true")
		t.Setenv(EnvDisable, "true")
		s, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		defer s.Close()
		if _, ok := s.(NopSink); !ok {
			t.Fatalf("want NopSink, got %T", s)
		}
	})
}
