package audit

// Hash-chained, tamper-EVIDENT audit sink (ADR-0056), the named follow-on to the
// durable JSONL sink (ADR-0047). The plain JSONL sink is append-only and retained
// but carries no cryptographic proof that a past record was not edited, removed,
// or reordered. This sink adds that proof by linking records into a hash chain:
// each record commits to the prior record's digest, so any change to the history
// breaks the chain and is caught by VerifyChain.
//
// Construction. For each record i the on-disk line is the Event fields PLUS two
// chain fields:
//
//	prev_hash = the previous record's hash (record 0 chains from GenesisHash)
//	hash      = SHA-256( prev_hash-bytes || canonical(event) )
//
// where canonical(event) is the deterministic JSON encoding of the Event ALONE
// (Go marshals struct fields in a fixed order with no maps, so this is stable and
// reproducible). The digest binds both the record's own content and its position
// behind the previous record: editing any past Event changes its hash; removing or
// reordering a record breaks the prev_hash == previous.hash linkage. Both are
// detected by re-verifying the chain.
//
// Honest evidence-vs-proof boundary. This is tamper-EVIDENT, not tamper-PROOF. An
// attacker with write access to the file can recompute the ENTIRE chain forward
// after an edit (producing a self-consistent but forged history), or simply
// truncate the tail (dropping the most recent records) — a verifier with only the
// file cannot distinguish a legitimately short chain from a truncated one without
// an external anchor. Closing that residual requires a true append-only/WORM store
// (object-lock/retention) or periodic notarization of the head digest to an
// independent system. See ADR-0056.
//
// Privacy is unchanged: a hash-chained record carries the same identity + request
// metadata as Event and adds only two hex digests — never a body, response,
// header, secret, or PII.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// GenesisHash is the prev_hash of the first record in a chain: 64 hex zeros (the
// hex encoding of a 32-byte all-zero SHA-256-width value). It is a fixed, public
// anchor so a verifier can confirm record 0 was not removed (its prev_hash must
// equal GenesisHash). A deployment that prefers a per-boot nonce genesis can build
// its own sink and verify with VerifyChainFrom; the env-driven default uses this.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// maxLineBytes bounds a single scanned record line during verification and
// last-hash recovery. Audit records are small (identity + metadata + two digests),
// so 1 MiB is far more than enough while still guarding against a pathological line.
const maxLineBytes = 1 << 20

// chainedRecord is the on-disk shape of one hash-chained audit line: the flattened
// Event fields (embedded, so they appear at the top level of the JSON object)
// followed by the two chain digests. canonical(event) for hashing is json.Marshal
// of the embedded Event ALONE, which a verifier reproduces after unmarshaling.
type chainedRecord struct {
	Event
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// chainHash computes hash = SHA-256( prevHash-bytes || canonicalEvent ) as a hex
// string. prevHash is hashed as its hex-string bytes (unambiguous and stable); the
// verifier recomputes identically from the stored prev_hash and re-marshaled Event.
func chainHash(prevHash string, canonicalEvent []byte) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write(canonicalEvent)
	return hex.EncodeToString(h.Sum(nil))
}

// HashChainedSink is a tamper-evident audit sink: it writes each Event as a JSON
// line carrying prev_hash + hash, chaining every record to its predecessor. It
// reuses the JSONLSink's O_APPEND file, write mutex, and fsync-on-Close machinery
// (composition), and adds its own mutex so the read-prev/compute-hash/write/
// update-prev sequence is atomic under concurrency — concurrent Records therefore
// produce a single well-ordered chain with no gaps or races.
type HashChainedSink struct {
	mu       sync.Mutex
	inner    *JSONLSink
	prevHash string
}

// NewHashChainedSink opens (creating if needed) an append-only, hash-chained audit
// log at path. If the file already contains a chain, it resumes from the last
// record's hash so a restart continues the SAME chain (rather than starting a new,
// unlinked one). If any existing line is unparseable, it fails closed: an operator
// must investigate before appending to a possibly-corrupted chain.
func NewHashChainedSink(path string) (*HashChainedSink, error) {
	prev, err := lastHash(path)
	if err != nil {
		return nil, err
	}
	inner, err := NewJSONLSink(path)
	if err != nil {
		return nil, err
	}
	return &HashChainedSink{inner: inner, prevHash: prev}, nil
}

// lastHash returns the hash of the final record in the chain file at path, or
// GenesisHash if the file does not exist or is empty. An unparseable record is a
// hard error (fail closed).
func lastHash(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return GenesisHash, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()

	last := GenesisHash
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	line := 0
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		line++
		if len(b) == 0 {
			continue
		}
		var rec chainedRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return "", fmt.Errorf("audit: existing hash-chain log %q has an unparseable record at line %d: %w", path, line, err)
		}
		last = rec.Hash
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("audit: reading existing hash-chain log %q: %w", path, err)
	}
	return last, nil
}

// Record appends one hash-chained line for e. The prev-hash read, digest
// computation, line write, and prev-hash update happen under a single mutex so
// concurrent callers extend one consistent chain. A marshal or write failure is
// logged and the event is dropped best-effort (as with JSONLSink) — and, crucially,
// prevHash is NOT advanced on a write failure, so the chain stays intact for the
// next successful append rather than referencing a line that never landed.
func (s *HashChainedSink) Record(e Event) {
	canon, err := json.Marshal(e)
	if err != nil {
		slog.Error("audit: marshal event failed; dropping", "err", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	hash := chainHash(s.prevHash, canon)
	rec := chainedRecord{Event: e, PrevHash: s.prevHash, Hash: hash}
	line, err := json.Marshal(rec)
	if err != nil {
		slog.Error("audit: marshal chained record failed; dropping", "err", err)
		return
	}
	line = append(line, '\n')
	if werr := s.inner.writeLine(line); werr != nil {
		slog.Error("audit: write chained event failed; dropping", "err", werr)
		return
	}
	s.prevHash = hash
}

// Close fsyncs and closes the underlying file (delegated to the inner JSONLSink).
func (s *HashChainedSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Close()
}

// VerifyChain walks the hash-chained audit stream in r and confirms it is intact,
// assuming the default GenesisHash anchor. It returns:
//
//   - ok=true, brokenAt=-1                 when every record links and hashes correctly;
//   - ok=false, brokenAt=i (0-based index) at the FIRST record that fails — because
//     its content was edited (recomputed hash mismatch), because it does not link to
//     its predecessor (prev_hash mismatch, e.g. a record was removed or reordered),
//     or because record 0's prev_hash is not GenesisHash (record 0 was removed).
//
// A non-nil err indicates the stream could not be read or a line could not be
// parsed (the parse index is reported in brokenAt). NOTE the honest limit: a
// verifier holding only the file cannot detect TAIL TRUNCATION (a valid prefix of
// a longer chain still verifies) — detecting that needs an external head anchor.
func VerifyChain(r io.Reader) (ok bool, brokenAt int, err error) {
	return VerifyChainFrom(r, GenesisHash)
}

// VerifyChainFrom is VerifyChain with an explicit genesis anchor, for deployments
// that chained from a per-boot nonce instead of GenesisHash.
func VerifyChainFrom(r io.Reader, genesis string) (ok bool, brokenAt int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	expectedPrev := genesis
	idx := 0
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue // tolerate blank lines; they are not records
		}
		var rec chainedRecord
		if uerr := json.Unmarshal(b, &rec); uerr != nil {
			return false, idx, fmt.Errorf("audit: unparseable record at index %d: %w", idx, uerr)
		}
		// Linkage: this record must point at its predecessor's hash (or genesis).
		if rec.PrevHash != expectedPrev {
			return false, idx, nil
		}
		// Integrity: recompute hash over the stored prev_hash and the canonical Event.
		canon, merr := json.Marshal(rec.Event)
		if merr != nil {
			return false, idx, fmt.Errorf("audit: re-marshal event at index %d: %w", idx, merr)
		}
		if chainHash(rec.PrevHash, canon) != rec.Hash {
			return false, idx, nil
		}
		expectedPrev = rec.Hash
		idx++
	}
	if serr := sc.Err(); serr != nil {
		return false, idx, fmt.Errorf("audit: reading chain: %w", serr)
	}
	return true, -1, nil
}

// VerifyFile opens the hash-chained audit log at path and verifies it with
// VerifyChain. It is the convenience entry point for an operator tool (see
// cmd/audit-verify).
func VerifyFile(path string) (ok bool, brokenAt int, err error) {
	f, err := os.Open(path)
	if err != nil {
		// An open failure is an I/O error, not a chain break: report brokenAt=-1
		// so callers do not misread it as "record 0 is bad".
		return false, -1, err
	}
	defer f.Close()
	return VerifyChain(f)
}
