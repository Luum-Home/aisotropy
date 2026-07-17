---
type: ADR
title: "ADR-0044 — Working-memory at-rest encryption: opt-in BadgerDB AES"
description: The on-disk BadgerDB working-memory tier gains real at-rest encryption via BadgerDB's native AES (Options.WithEncryptionKey), opted in with the AISOTROPY_DATA_ENCRYPTION_KEY environment variable (hex or base64, 16/24/32 bytes for AES-128/192/256). When unset the store behaves exactly as before (documented opt-in); when set alongside AISOTROPY_DATA_DIR every SSTable and value-log entry is encrypted on disk. Key material is never logged. Closes the working-memory half of the "no app-layer at-rest encryption" security gap.
tags: [decisions, adr, security, encryption, at-rest, badgerdb, working-memory, gdpr]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0044 — Working-memory at-rest encryption: opt-in BadgerDB AES

## Status

✅ **Accepted (2026-07-17).** Security fix from a data-governance audit (Gap 5). Adds an
opt-in capability; the default (no key) boot is byte-for-byte unchanged. Complements
[ADR-0045](0045-facts-tier-at-rest-encryption-posture.md) (the facts SQLite tier's honest
disk-encryption posture) and [ADR-0046](0046-subject-level-erasure-gdpr-art17.md) (subject erasure).

## Context

The working-memory tier (`internal/memory`, embedded BadgerDB) persists the live chat buffer and
its condensed summary to disk whenever `AISOTROPY_DATA_DIR` is set. Before this decision those bytes
were written in **plaintext**: confidentiality of chat content at rest depended entirely on operator
disk encryption, which was undocumented and unenforced. A stolen disk or an unencrypted volume
snapshot would expose raw conversation history.

BadgerDB ships **native, transparent AES encryption** (`Options.WithEncryptionKey`, which encrypts
both SSTables and the value log) and requires only a non-zero index cache — which
`tuneForWorkingMemory` already configures. This is real cryptography in the storage engine, not an
application-level wrapper we would have to hand-roll and get wrong.

## Decision

- Add `memory.OpenEncrypted(path, maxActiveMessages, summarizer, key)` — same as `memory.Open` but
  with `WithEncryptionKey(key)` applied. The key length is validated to 16, 24, or 32 bytes
  (AES-128/192/256) before opening; a wrong length is a clear error, not a deferred BadgerDB panic.
- Opt in via the `AISOTROPY_DATA_ENCRYPTION_KEY` environment variable, decoded as **hex or base64**
  (hex tried first). It is wired in `aisotropy/encryption.go` (`parseDataEncryptionKey`) and consumed
  by `newWorkingMemory` in `aisotropy/app.go`: encryption engages only when the key is set **and**
  `AISOTROPY_DATA_DIR` points at an on-disk store.
- **Key material is never logged.** `parseDataEncryptionKey` keeps the raw value out of every error
  (only the decoded byte length is ever surfaced), and the boot log records `key_bits` only.
- Postures made loud at boot: on-disk **without** a key logs a warning naming both this ADR and the
  disk-encryption compensating control; a key set **without** `AISOTROPY_DATA_DIR` (in-memory store)
  logs that encryption cannot apply.

## Consequences

- **Positive:** chat content at rest can be AES-encrypted with a single env var, no new dependency
  (BadgerDB is already the working-memory engine, Apache-2.0). Reopening requires the same key —
  BadgerDB refuses a wrong or absent key — so a lost key means lost data, which is the correct
  security property (proven in `internal/memory/erase_encrypt_test.go`).
- **Negative / honest deltas:** (1) the in-memory default (no `AISOTROPY_DATA_DIR`) has no disk and
  therefore no at-rest concern — unchanged. (2) Key **rotation** and a KMS/secret-manager integration
  are out of scope; the operator supplies raw key material via env and owns its lifecycle. (3) The
  **facts** SQLite tier is a separate story — the pure-Go driver cannot do SQLCipher — handled
  honestly in [ADR-0045](0045-facts-tier-at-rest-encryption-posture.md), NOT by faking a PRAGMA.

## Alternatives considered

- **Always-on encryption.** Rejected: forces every operator to manage a key even for local dev and
  breaks the zero-config default; opt-in preserves the walking-skeleton experience.
- **Application-layer envelope encryption in the memory package.** Rejected: re-implements what the
  storage engine already does correctly, and would not cover the value log / LSM internals.
