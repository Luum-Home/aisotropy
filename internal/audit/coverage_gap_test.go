package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file closes remaining statement-coverage gaps in the audit package's
// error-handling and edge-case branches not reached by audit_test.go /
// hashchain_test.go.

// --- audit.go: FromEnv's HashChainedSink-open error branch -----------------

// TestFromEnvHashChainOpenFails covers FromEnv's "audit: open tamper-evident
// hash-chain log" wrap: AISOTROPY_AUDIT_HASHCHAIN + a path NewHashChainedSink
// cannot open (its parent directory does not exist) must fail the boot.
func TestFromEnvHashChainOpenFails(t *testing.T) {
	t.Setenv(EnvDisable, "")
	t.Setenv(EnvLogPath, filepath.Join(t.TempDir(), "missing-subdir", "a.jsonl"))
	t.Setenv(EnvHashChain, "true")
	_, err := FromEnv()
	if err == nil || !strings.Contains(err.Error(), "open tamper-evident hash-chain log") {
		t.Fatalf("FromEnv with an unopenable hash-chain path = %v, want a wrapped open error", err)
	}
}

// --- audit.go: JSONLSink marshal / write / close branches ------------------

// TestJSONLSinkRecordMarshalError covers Record's json.Marshal error branch: a
// Timestamp whose year is out of range fails to encode (time.Time.MarshalJSON
// rejects years outside [0,9999]), and Record must log and drop rather than
// write a partial or corrupt line.
func TestJSONLSinkRecordMarshalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	defer sink.Close()

	e := sampleEvent()
	e.Timestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	sink.Record(e) // must not panic

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(bytes.TrimSpace(data)) != 0 {
		t.Fatalf("file has content after a marshal-error record = %q, want empty (dropped, not written)", data)
	}
}

// TestJSONLSinkRecordAfterCloseIsDropped covers both writeLine's "sink is
// closed" branch and Record's write-failure logging branch: Record on a
// closed sink must not panic and must not resurrect the file.
func TestJSONLSinkRecordAfterCloseIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if werr := sink.writeLine([]byte("x\n")); werr == nil || !strings.Contains(werr.Error(), "sink is closed") {
		t.Fatalf("writeLine after Close = %v, want \"sink is closed\"", werr)
	}
	sink.Record(sampleEvent()) // must not panic despite the closed sink
}

// TestJSONLSinkDoubleCloseIsNoop covers Close's "already closed" (s.f == nil)
// short-circuit: closing twice must not error the second time.
func TestJSONLSinkDoubleCloseIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil (no-op on an already-closed sink)", err)
	}
}

// TestJSONLSinkCloseLogsSyncFailure covers Close's Sync-error logging branch:
// the underlying fd is closed out from under the sink (leaving s.f non-nil),
// so the subsequent Sync() call inside Close fails — the failure must be
// logged, not silently swallowed, and Close must still return the eventual
// Close() error rather than panicking.
func TestJSONLSinkCloseLogsSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	if err := sink.f.Close(); err != nil {
		t.Fatalf("external fd close: %v", err)
	}
	if err := sink.Close(); err == nil {
		t.Fatal("Close over an externally-closed fd = nil, want the underlying close error to surface")
	}
}

// --- audit.go: NopSink.Record --------------------------------------------

// TestNopSinkRecordDiscards exercises NopSink.Record directly — it is never
// invoked through FromEnv's own tests, only constructed and Closed.
func TestNopSinkRecordDiscards(t *testing.T) {
	var s Sink = NopSink{}
	s.Record(sampleEvent()) // must not panic; nothing to assert beyond that
}

// --- hashchain.go: lastHash branches ----------------------------------------

// TestLastHashSkipsBlankLines covers lastHash's "len(b) == 0: continue"
// branch: blank lines in an existing chain file must be tolerated, not
// treated as a corrupt record.
func TestLastHashSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatalf("seed blank file: %v", err)
	}
	got, err := lastHash(path)
	if err != nil {
		t.Fatalf("lastHash over blank lines: %v", err)
	}
	if got != GenesisHash {
		t.Fatalf("lastHash over blank lines = %q, want GenesisHash (no real records)", got)
	}
}

// TestLastHashLineTooLong covers lastHash's scanner-error wrap branch
// (sc.Err() != nil): a single line past maxLineBytes overflows the scanner's
// buffer and must fail closed rather than silently truncate/skip it.
func TestLastHashLineTooLong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	big := bytes.Repeat([]byte("x"), 2<<20) // > maxLineBytes, no newline
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatalf("seed oversized file: %v", err)
	}
	_, err := lastHash(path)
	if err == nil || !strings.Contains(err.Error(), "reading existing hash-chain log") {
		t.Fatalf("lastHash over an oversized line = %v, want a wrapped scanner error", err)
	}
}

// TestLastHashPermissionDenied covers lastHash's non-NotExist Open-error
// branch: an existing but unreadable file (0o000 permissions) makes os.Open
// fail with a permission error, which is neither ErrNotExist nor nil and must
// be surfaced as-is.
func TestLastHashPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits, so this file would still be readable")
	}
	path := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o000); err != nil {
		t.Fatalf("seed unreadable file: %v", err)
	}
	_, err := lastHash(path)
	if err == nil {
		t.Fatal("lastHash over a permission-denied file = nil error, want a permission error")
	}
	if strings.Contains(err.Error(), "unparseable") || strings.Contains(err.Error(), "reading existing hash-chain") {
		t.Fatalf("lastHash permission error = %v, want a raw Open error, not the scan/parse wrap", err)
	}
}

// TestNewHashChainedSinkJSONLOpenFails covers NewHashChainedSink's own
// open-error branch DISTINCT from lastHash's: lastHash succeeds (the path
// simply does not exist yet, so it returns GenesisHash, nil), but the
// subsequent NewJSONLSink call fails because the path's parent directory does
// not exist either.
func TestNewHashChainedSinkJSONLOpenFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-subdir", "a.jsonl")
	_, err := NewHashChainedSink(path)
	if err == nil {
		t.Fatal("NewHashChainedSink with an unopenable path = nil error, want failure")
	}
}

// TestHashChainRecordMarshalError covers HashChainedSink.Record's OWN
// json.Marshal(e) error branch — distinct from JSONLSink's, which is a
// different Record method entirely. Same trick: a Timestamp year outside
// [0,9999] fails to encode.
func TestHashChainRecordMarshalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	defer sink.Close()

	e := sampleEvent()
	e.Timestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	sink.Record(e) // must not panic

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(bytes.TrimSpace(data)) != 0 {
		t.Fatalf("chain file has content after a marshal-error record = %q, want empty (dropped)", data)
	}
}

// --- hashchain.go: Record's inner-write-failure branch ----------------------

// TestHashChainRecordWriteFailureIsDropped covers Record's "write chained
// event failed; dropping" branch AND proves prevHash is NOT advanced on a
// failed write (so the chain stays intact for the next successful append):
// closing the inner JSONLSink out from under the HashChainedSink makes every
// subsequent Record's write fail.
func TestHashChainRecordWriteFailureIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	before := sink.prevHash

	if err := sink.inner.Close(); err != nil {
		t.Fatalf("close inner sink: %v", err)
	}
	sink.Record(sampleEvent()) // must not panic; write fails and is dropped

	if sink.prevHash != before {
		t.Fatalf("prevHash advanced after a failed write: before=%q after=%q, want unchanged", before, sink.prevHash)
	}
}

// --- hashchain.go: VerifyChainFrom branches ---------------------------------

// TestVerifyChainFromSkipsBlankLines covers VerifyChainFrom's blank-line
// tolerance branch: a well-formed chain with blank lines interspersed still
// verifies.
func TestVerifyChainFromSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	sink, err := NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	sink.Record(sampleEvent())
	sink.Record(sampleEvent())
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	withBlanks := append([]byte("\n"), data...)
	withBlanks = append(withBlanks, '\n')

	ok, brokenAt, verr := VerifyChain(bytes.NewReader(withBlanks))
	if verr != nil {
		t.Fatalf("VerifyChain with blank lines: %v", verr)
	}
	if !ok || brokenAt != -1 {
		t.Fatalf("VerifyChain with blank lines = ok=%v brokenAt=%d, want an intact chain", ok, brokenAt)
	}
}

// TestVerifyChainFromUnparseableLine covers the json.Unmarshal error branch:
// a syntactically invalid line must fail with a parse error naming its index,
// not be silently skipped.
func TestVerifyChainFromUnparseableLine(t *testing.T) {
	ok, brokenAt, err := VerifyChain(strings.NewReader("{not valid json}\n"))
	if err == nil || !strings.Contains(err.Error(), "unparseable record at index 0") {
		t.Fatalf("VerifyChain(unparseable) = (ok=%v brokenAt=%d err=%v), want an unparseable-record error", ok, brokenAt, err)
	}
}

// TestVerifyChainFromPrevHashMismatch covers the prev_hash linkage-mismatch
// branch directly (distinct from the removed/reordered-record integration
// tests in hashchain_test.go): a hand-built record whose prev_hash does not
// match the expected genesis breaks at index 0 with no parse error.
func TestVerifyChainFromPrevHashMismatch(t *testing.T) {
	e := sampleEvent()
	canon, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	rec := chainedRecord{Event: e, PrevHash: "not-the-genesis-hash", Hash: chainHash("not-the-genesis-hash", canon)}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}

	ok, brokenAt, verr := VerifyChain(bytes.NewReader(line))
	if verr != nil {
		t.Fatalf("VerifyChain: %v", verr)
	}
	if ok || brokenAt != 0 {
		t.Fatalf("VerifyChain(wrong prev_hash) = ok=%v brokenAt=%d, want ok=false brokenAt=0", ok, brokenAt)
	}
}

// TestVerifyChainFromHashMismatch covers the integrity (recomputed-hash)
// mismatch branch directly: prev_hash correctly points at genesis, but the
// stored hash does not match SHA-256(prev_hash || canonical(event)).
func TestVerifyChainFromHashMismatch(t *testing.T) {
	e := sampleEvent()
	rec := chainedRecord{Event: e, PrevHash: GenesisHash, Hash: "0000000000000000000000000000000000000000000000000000000000dead"}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}

	ok, brokenAt, verr := VerifyChain(bytes.NewReader(line))
	if verr != nil {
		t.Fatalf("VerifyChain: %v", verr)
	}
	if ok || brokenAt != 0 {
		t.Fatalf("VerifyChain(forged hash) = ok=%v brokenAt=%d, want ok=false brokenAt=0", ok, brokenAt)
	}
}

// --- hashchain.go: VerifyFile open-error branch -----------------------------

// TestVerifyChainScannerLineTooLong covers VerifyChainFrom's OWN scanner-error
// wrap ("audit: reading chain") — distinct from lastHash's identical guard
// over the SAME maxLineBytes limit, but a different call site: a line fed
// straight to VerifyChain (not via a file NewHashChainedSink resumes from)
// that exceeds maxLineBytes overflows the scanner buffer.
func TestVerifyChainScannerLineTooLong(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2<<20) // no newline, single oversized token
	ok, brokenAt, err := VerifyChain(bytes.NewReader(big))
	if err == nil || !strings.Contains(err.Error(), "reading chain") {
		t.Fatalf("VerifyChain over an oversized line = (ok=%v brokenAt=%d err=%v), want a wrapped reading-chain error", ok, brokenAt, err)
	}
}

// TestVerifyFileMissingPath covers VerifyFile's os.Open error branch: a
// nonexistent path must report brokenAt=-1 (an I/O error, not a chain break)
// alongside the error.
func TestVerifyFileMissingPath(t *testing.T) {
	ok, brokenAt, err := VerifyFile(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err == nil {
		t.Fatal("VerifyFile(missing path) = nil error, want an open error")
	}
	if ok || brokenAt != -1 {
		t.Fatalf("VerifyFile(missing path) = ok=%v brokenAt=%d, want ok=false brokenAt=-1", ok, brokenAt)
	}
}

// --- documented uncoverable branches ------------------------------------------
//
// hashchain.go HashChainedSink.Record's second json.Marshal (of the full
// chainedRecord, at the "marshal chained record failed; dropping" branch) is
// not reachable through the public API: chainedRecord embeds the SAME Event
// value already successfully marshaled a few lines earlier (as `canon`), plus
// two plain strings (PrevHash/Hash). Nothing about wrapping that already-
// encodable Event in a struct with two more strings can make json.Marshal
// newly fail — whatever would make the second call fail would have already
// failed the first (TestHashChainRecordMarshalError above exercises that
// first, reachable marshal-error branch).
//
// hashchain.go VerifyChainFrom's "re-marshal event at index %d" branch (the
// json.Marshal(rec.Event) error immediately after a successful
// json.Unmarshal) is also not reachable: the one concrete way to make
// time.Time fail MarshalJSON — a year outside [0,9999] — cannot reach that
// point, because time.Time's UnmarshalJSON parses via a strict RFC3339
// layout that itself rejects a 5-digit year during decode (verified:
// unmarshaling `"10000-01-01T00:00:00Z"` fails at the parse step with "cannot
// parse ... as \"-\"", never reaching a decoded Timestamp that could then
// fail to re-encode). No other Event field can make an already-valid decoded
// struct fail to re-marshal.
