---
type: ADR
title: "ADR-0056 — Tamper-evident hash-chained audit sink (follow-on to ADR-0047)"
description: The durable JSONL access-audit sink (ADR-0047) is append-only and retained but NOT cryptographically tamper-evident — an operator or an attacker with write access can edit or truncate it and nothing proves they did not. This ADR adds an opt-in HashChainedSink (AISOTROPY_AUDIT_HASHCHAIN) where each record commits to the prior record's digest (hash = SHA-256(prev_hash || canonical(event))), plus a VerifyChain verifier and an audit-verify CLI, so edits, removals, and reorders of PAST records are detectable. Honest boundary — tamper-EVIDENT, not tamper-PROOF: an attacker with write access can recompute the chain forward or truncate the tail; a WORM store or external head-anchor notarization is the further follow-on.
tags: [decisions, adr, security, audit, tamper-evidence, hash-chain, soc2, iso27001, compliance, honesty]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0056 — Tamper-evident hash-chained audit sink (follow-on to ADR-0047)

## Status

✅ **Accepted (2026-07-17).** The named follow-on to the durable access-audit log
([ADR-0047](0047-durable-access-audit-log.md)), which explicitly deferred cryptographic
tamper-evidence as future work. This ADR closes the *tamper-evidence* half of that gap for
edits, removals, and reorders of past records; it does **not** claim tamper-proofness (see
[Honest boundary](#honest-boundary-evidence-vs-proof)).

## Context

ADR-0047 added `internal/audit`: an append-only, retained access trail (one `Event` per served
`/v1` request, identity + request metadata **only**) with a durable JSONL file sink selected by
`AISOTROPY_AUDIT_LOG`. It was deliberately honest about its residual:

> the JSONL file sink is append-only and retained, but it is **NOT cryptographically
> tamper-evident** — an operator (or an attacker with write access to the file) can edit or
> truncate it, and nothing in the file proves that it was not.

For an auditor mapping to **SOC 2 (CC7.2 monitoring)** or **ISO 27001 (A.12.4 logging &
monitoring, A.12.4.2 protection of log information)**, "retained" is necessary but not
sufficient: A.12.4.2 asks that logging facilities and log information be **protected against
tampering**. A plain JSONL file offers no evidence that a historical record was not silently
rewritten or dropped. That is the gap this ADR addresses.

## Decision

Add an **opt-in, composable** hash-chained sink behind the existing `Sink` / `WithAuditSink`
seam — no change to the default behavior, no new dependency (stdlib `crypto/sha256` only).

### Chain construction

Each on-disk line is the existing `Event` fields **plus** two chain fields:

```
prev_hash = the previous record's hash   (record 0 chains from a genesis anchor)
hash      = SHA-256( prev_hash-bytes || canonical(event) )
```

- **`canonical(event)`** is `json.Marshal` of the `Event` **alone**. Go marshals struct fields
  in a fixed order with no maps, and `time.Time` round-trips through RFC 3339 (dropping the
  monotonic reading), so the encoding is deterministic and a verifier reproduces it exactly
  after unmarshaling the stored record.
- **`prev_hash`** is fed to the digest as its hex-string bytes — unambiguous and stable, and
  identically recomputed by the verifier from the stored `prev_hash`.
- **Genesis.** Record 0's `prev_hash` is a fixed public anchor, `GenesisHash` = 64 hex zeros, so
  a verifier can confirm record 0 was not removed (its `prev_hash` must equal the anchor). A
  deployment preferring a per-boot nonce genesis can build its own sink and verify with
  `VerifyChainFrom(reader, genesis)`.

The digest binds **both** a record's content **and** its position behind its predecessor:

- editing any past `Event` changes its recomputed `hash` → mismatch with the stored `hash`;
- removing or reordering a record breaks the `prev_hash == previous.hash` linkage.

Both are detected by re-walking the chain.

### Verifier

```go
VerifyChain(r io.Reader) (ok bool, brokenAt int, err error)          // default GenesisHash anchor
VerifyChainFrom(r io.Reader, genesis string) (ok bool, brokenAt int, err error)
VerifyFile(path string) (ok bool, brokenAt int, err error)           // convenience for the CLI
```

`VerifyChain` returns `ok=true, brokenAt=-1` for an intact chain, or `ok=false, brokenAt=i`
(0-based) at the **first** record that fails — content edited (hash mismatch), unlinked
(`prev_hash` mismatch from a removal/reorder), or record 0 not matching the genesis anchor. A
non-nil `err` signals an unreadable stream or an unparseable line (index in `brokenAt`).

An operator/auditor tool ships at `internal/audit/cmd/audit-verify` (exit 0 = intact, 1 = broken,
2 = usage/I-O error).

### Opt-in wiring

`FromEnv` gains one lever, `AISOTROPY_AUDIT_HASHCHAIN`:

| `AISOTROPY_AUDIT_DISABLE` | `AISOTROPY_AUDIT_HASHCHAIN` | `AISOTROPY_AUDIT_LOG` | Sink |
|---|---|---|---|
| truthy | — | — | `NopSink` (explicit opt-out wins) |
| — | truthy | set | **`HashChainedSink`** (tamper-evident) |
| — | truthy | unset | **error — fail closed** (a chain needs a durable file) |
| — | — | set | `JSONLSink` (durable, ADR-0047) |
| — | — | unset | `SlogSink` (default access trail) |

The default is unchanged: unless an operator sets `AISOTROPY_AUDIT_HASHCHAIN` **and** a durable
path, they get exactly the ADR-0047 behavior. `HashChainedSink` resumes an existing chain on
restart (reads the last record's hash) and **fails closed** if an existing record is unparseable,
so it never silently appends to a corrupted chain.

### Privacy is unchanged

A hash-chained record carries the same identity + request metadata as `Event` and adds only two
hex digests — never a body, response, header, secret, or PII. A schema-guard test enforces the
allowed field set (`Event` fields + `prev_hash` + `hash`) and scans for forbidden substrings.

### Concurrency

`HashChainedSink` reuses `JSONLSink`'s `O_APPEND` file, write mutex, and fsync-on-`Close` (by
composition) and adds its own mutex so the read-prev → compute-hash → write → update-prev
sequence is atomic. Concurrent `Record`s therefore extend **one** well-ordered chain with no gaps
or races (verified under `go test -race`). On a write failure the previous-hash pointer is **not**
advanced, so a dropped write cannot leave the chain referencing a line that never landed.

## Honest boundary: evidence vs. proof

This is tamper-**EVIDENT**, not tamper-**PROOF**. With only the file, an attacker who holds write
access can:

1. **Recompute the whole chain forward** after an edit — producing a self-consistent but forged
   history that `VerifyChain` accepts. The chain proves *no partial/uncoordinated edit slipped
   through*; it does **not** prove *the writer never rewrote everything*.
2. **Truncate the tail** — dropping the most recent records. A valid prefix of a longer chain
   still verifies, and a verifier holding only the file cannot distinguish a legitimately short
   chain from a truncated one.

Closing that residual requires making the substrate itself resist rewriting or anchoring the head
externally, neither of which a single local file can provide:

- a true **append-only / WORM store** (object-lock / retention policy) so past bytes are
  immutable to the writer; and/or
- **periodic external notarization** of the head `hash` to an independent system (a second
  operator's store, a transparency log, a timestamping authority), so a forward-recompute or a
  truncation is caught by comparing against the anchored head.

Those are the **further follow-on**, tracked here so no reader mistakes this sink for tamper-proof
storage. This ADR deliberately delivers the local, dependency-free, verifiable increment first.

## Consequences

**Positive.** Edits, removals, and reorders of any past record become detectable with a single
`audit-verify` run; no new dependency (stdlib crypto only); default behavior and the privacy
contract are unchanged; opt-in and composable behind the existing seam; restart-safe.

**Negative / residual.** Not tamper-proof (see above); tail truncation and full-chain
recomputation remain undetectable without a WORM substrate or an external head anchor;
verification is an O(n) re-walk (acceptable for an audit-time/operator tool, not a hot path). The
two extra hex digests add ~140 bytes per record.

## Alternatives considered

- **Per-record detached signatures (HMAC / asymmetric).** Detects content edits but not
  removal/reorder unless the signature also covers position; needs key management. A hash chain
  gives ordering and linkage for free with no key. Rejected for this increment; an HMAC/signed
  head is compatible as a later layer.
- **Merkle tree over records.** Enables efficient inclusion proofs but is heavier than needed for
  a linear, append-only access log; a chain is the degenerate, simplest correct case.
- **Delegate entirely to a WORM object store.** The stronger control, but it is an operator
  deployment decision (infrastructure), not something the runtime can guarantee; the hash chain
  is the in-runtime, portable increment that *also composes with* a WORM store later.

## References

- [ADR-0047](0047-durable-access-audit-log.md) — durable access-audit log (this ADR's parent).
- `internal/audit/hashchain.go`, `internal/audit/hashchain_test.go`,
  `internal/audit/cmd/audit-verify/main.go`.
- ISO 27001 A.12.4 (logging & monitoring), A.12.4.2 (protection of log information); SOC 2 CC7.2.
