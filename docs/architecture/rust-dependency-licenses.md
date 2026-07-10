---
type: Reference
title: "Rust dependency license audit (crates/sleep-cycle)"
description: The constitutional license gate applied to the 274-crate transitive dependency tree of the DataFusion sleep-cycle worker. Result — 100% permissive-compatible; no forced copyleft.
tags: [licenses, rust, datafusion, supply-chain, constitutional-gate]
timestamp: 2026-07-10T00:00:00Z
---

# Rust dependency license audit — crates/sleep-cycle

The DataFusion sleep-cycle worker ([`crates/sleep-cycle`](../../crates/sleep-cycle), ADR-0005) pulls
the Arrow/DataFusion/tokio ecosystem — **274 transitive crates**. This is the constitutional
license-gate review ([license-policy](../../.claude/rules/cos/license-policy.md),
[constitutional-gates](../../.claude/rules/constitutional-gates.md)) that was flagged as pending in
the [phases-3-7-as-built debt ledger](phases-3-7-as-built.md).

## Method

`cargo metadata --format-version 1 --manifest-path crates/sleep-cycle/Cargo.toml`, extracting each
package's SPDX `license` field and flagging any copyleft / source-available term (GPL/AGPL/SSPL/
BUSL/BSL/Commons-Clause). Reproduce anytime with that command. (`cargo-deny` is the CI-grade tool;
it was not installed in the audit environment, so the check was done directly from `cargo metadata`.)

## Result — PASS: 100% permissive-compatible

| Count | License |
|---|---|
| 131 | MIT OR Apache-2.0 |
| 38 | Apache-2.0 |
| 29 | MIT |
| 18 | MIT/Apache-2.0 |
| 18 | Unicode-3.0 |
| 8 | Apache-2.0 OR MIT |
| 5 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT |
| 5 | BSD-3-Clause |
| 4 | Unlicense OR MIT |
| 4 | Unlicense/MIT |
| ~10 | other permissive combinations (BSD-2, 0BSD, CC0, Zlib, MIT-0) |

**No forced copyleft. No AGPL/GPL/SSPL/pure-BSL/Commons-Clause.** Two families deserve a one-line note
because a scanner might flag them:

- **`ryu` — `Apache-2.0 OR BSL-1.0`.** An `OR` dual-license: we exercise the **Apache-2.0** option,
  which carries no BSL obligation. Not a restriction.
- **2 crates — `MIT OR Apache-2.0 OR LGPL-2.1-or-later`.** Again `OR`: we take MIT/Apache-2.0; the
  LGPL option is never selected.
- **`Unicode-3.0` (18 crates, e.g. `unicode-ident`).** The Unicode license — a permissive,
  attribution-only license, safe for commercial/closed use.

## Conclusion

The Rust half of the stack upholds the repo's "100% MIT/Apache" posture: every crate offers a
permissive option and we take it. The debt-ledger item "274 Rust crates unreviewed for license" is
**closed**. Re-run the `cargo metadata` command above whenever `Cargo.lock` changes; wire `cargo-deny`
into CI when a Rust CI lane exists (a follow-up, not a blocker — the tree is clean today).
