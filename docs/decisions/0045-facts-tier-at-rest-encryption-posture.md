---
type: ADR
title: "ADR-0045 — Facts tier at-rest encryption: operator disk encryption as the honest compensating control"
description: The facts memory tier's embedded SQLite runs on the pure-Go, CGO-free modernc.org/sqlite driver, which has NO SQLCipher / PRAGMA key support. Rather than fake app-layer encryption, this ADR accepts operator disk/volume encryption (LUKS/dm-crypt/cloud-EBS) as the documented compensating control for the on-disk facts DB, and surfaces the posture with a loud startup warning. Honesty over a fake PRAGMA.
tags: [decisions, adr, security, encryption, at-rest, sqlite, facts, gdpr, honesty]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0045 — Facts tier at-rest encryption: operator disk encryption as the honest compensating control

## Status

✅ **Accepted (2026-07-17).** Security fix from a data-governance audit (Gap 5), paired with
[ADR-0044](0044-working-memory-at-rest-encryption.md) (working-memory AES). This ADR is the *honest*
half: it documents a **limitation** and its accepted compensating control rather than shipping a
capability.

## Context

The facts tier (`internal/facts`) stores distilled, durable statements about a user in embedded
SQLite + FTS5. The zero-config, local-first driver is **`modernc.org/sqlite`** — a pure-Go, CGO-free
translation of SQLite (BSD-3), chosen so the binary builds and runs with no C toolchain.

That driver has **no SQLCipher and no `PRAGMA key`**: there is no supported way to make it encrypt the
database file at the page level. The tempting move — emitting a `PRAGMA key` and pretending the DB is
encrypted — would be **actively harmful**: it would either be a silent no-op (a false sense of
security) or fail, while the audit checkbox reads "encrypted." The project's code-truth discipline
(`.claude/rules/prune-evidence-gate.md`) forbids asserting a security property the code does not
deliver.

## Decision

- Do **not** fake encryption on the facts SQLite tier. No `PRAGMA key`, no app-layer wrapper masquerading
  as page encryption.
- Accept **operator disk/volume encryption (LUKS/dm-crypt, cloud provider EBS/PD encryption)** as the
  compensating control for the on-disk facts database. This is the same control that protects any
  file-backed store and is standard practice for a self-hosted, single-binary deployment.
- **Surface the posture, don't bury it.** When `AISOTROPY_DATA_DIR` is set (on-disk SQLite facts),
  `openFactsStore` (`aisotropy/app.go`) logs a startup **warning** stating the facts DB is not
  app-encrypted and that disk/volume encryption is the compensating control, naming this ADR.
- The networked **rqlite** backend (`RQLITE_URL`) inherits the same posture: encryption there is the
  rqlite node's / underlying volume's responsibility, out of this process's control.

## Consequences

- **Positive:** no false security claim; the operator knows exactly what to configure (full-disk /
  volume encryption) and why. No new dependency, no CGO, the pure-Go build stays intact.
- **Negative / honest deltas:** the facts DB file is plaintext at the application layer; an attacker
  with read access to an **unencrypted** volume can read facts. This is strictly weaker than the
  working-memory tier's optional AES ([ADR-0044](0044-working-memory-at-rest-encryption.md)) — a
  deliberate, documented asymmetry driven by the driver's capabilities, not an oversight.

## Alternatives considered

- **Switch to a CGO SQLCipher driver (e.g. `mattn/go-sqlite3` + SQLCipher).** Rejected here: it
  reintroduces a C toolchain dependency and a build-matrix cost, contradicting the CGO-free posture.
  Left open as a future ADR if an adopter's compliance regime requires page-level DB encryption over
  disk encryption.
- **Encrypt fact `content` at the application layer before insert.** Rejected: it breaks FTS5 keyword
  search (the entire point of the tier — KB-over-RAG), since an FTS index over ciphertext is useless.
- **Fake a `PRAGMA key`.** Rejected outright — dishonest and forbidden by the code-truth gate.
