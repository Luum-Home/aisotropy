#!/usr/bin/env python3
"""
Adversarial isolation smoke test for Letta (MemGPT) — REAL SDK types.

What this proves (feature #1): Letta's multi-tenancy is *developer-passed*, not
framework-enforced. Every stored object below is a real
`letta_client.types.BlockResponse` / `Passage`, and the store's `list()` /
`search()` replicate the real server's optional `identity_id` / `project_id` /
`label` / `tags` filter contract. So the correct path and the leaky path are
genuinely one argument apart — which is the whole point.

Why offline: Letta agents + their memory live on a SERVER (`letta server` /
Docker / Letta Cloud). This environment has none and we do not fake a connection.
The block/passage data plane and its filter semantics are reconstructed with the
genuine SDK types; the LLM is replaced by a deterministic responder. If
LETTA_BASE_URL is set, we additionally attempt a real, read-only server ping and
report the result honestly (no fabricated success).

Pattern (mirrors aisotropy's isolation_e2e_test.go):
  1. Seed tenantA with a unique marker (a real BlockResponse + Passage).
  2. Positive control: tenantA reads its own scope -> marker MUST appear.
  3. Negative probe: tenantB reads with its OWN correct scope -> marker MUST NOT appear.
  4. Leaky paths (all real API surface): unscoped list / raw retrieve-by-id /
     unscoped archival search -> marker DOES appear = cross-tenant LEAK.
"""

from __future__ import annotations

import os
import secrets
import sys

# Import the real SDK up front so a missing dependency fails loudly.
try:
    import letta_client  # noqa: F401
    from letta_client.types import BlockResponse, Passage  # noqa: F401
except ImportError:
    print("ERROR: letta-client not installed. Run: pip install -r requirements.txt",
          file=sys.stderr)
    sys.exit(1)

# Make `orbit` importable when run as `python scripts/smoke.py`.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.config import load_config  # noqa: E402
from orbit.memory import BlockStore, PassageStore, TenantScope  # noqa: E402


def marker() -> str:
    return f"lt-{secrets.token_hex(8)}"


def run_matrix() -> tuple[bool, list[str]]:
    details: list[str] = []
    leaked = False

    blocks = BlockStore()
    passages = PassageStore()

    scope_a = TenantScope(tenant_id="acme", user_id="alice")
    scope_b = TenantScope(tenant_id="globex", user_id="bob")

    tok = marker()
    print(f"[letta] Seeding tenant 'acme' with marker: {tok}")

    # ── Seed tenantA (real BlockResponse + Passage) ──
    seeded = blocks.create(
        label="human", value=f"Card ending 4242, note: {tok}",
        project_id=scope_a.project_id, tags=scope_a.tags,
    )
    passages.insert(text=f"Prior ticket: {tok}", tags=scope_a.tags)
    assert isinstance(seeded, BlockResponse)

    # ── 1. Positive control: acme reads its OWN scope ──
    own = blocks.list(project_id=scope_a.project_id, tags=scope_a.tags)
    if any(tok in b.value for b in own):
        details.append("  OK  Positive control: acme saw its own marker")
        print("  OK  Positive control: acme saw its own marker")
    else:
        details.append("  FAIL Positive control failed — aborting.")
        print("  FAIL Positive control failed — aborting.")
        return False, details

    # ── 2. Negative probe: globex reads with its OWN correct scope ──
    other = blocks.list(project_id=scope_b.project_id, tags=scope_b.tags)
    if any(tok in b.value for b in other):
        details.append("  FAIL globex's correctly-scoped read saw acme's marker!")
        print("  FAIL globex's scoped read leaked!")
        leaked = True
    else:
        details.append("  OK  No leak: globex's correctly-scoped read is clean")
        print("  OK  No leak: globex correctly-scoped read is clean")

    # ── 3. Leaky path A: blocks.list() with NO filter (org-wide scan) ──
    print()
    print("[letta] Leaky path A — client.blocks.list() with no identity/project/tags:")
    org_wide = blocks.list()
    if any(tok in b.value for b in org_wide):
        details.append(
            "  LEAK blocks.list() (no filter) returned acme's block to any caller "
            f"({len(org_wide)} blocks org-wide)")
        print(f"  LEAK unscoped blocks.list() saw acme's marker ({len(org_wide)} blocks)")
        leaked = True

    # ── 4. Leaky path B: blocks.retrieve(id) — no tenant check ──
    print("[letta] Leaky path B — client.blocks.retrieve(block_id) from any caller:")
    grabbed = blocks.retrieve(seeded.id)
    if grabbed is not None and tok in grabbed.value:
        details.append(
            "  LEAK blocks.retrieve(id) performed no tenant check — raw id = cross read")
        print("  LEAK retrieve-by-id returned acme's block with no tenant check")
        leaked = True

    # ── 5. Leaky path C: archival search with no tag scope ──
    print("[letta] Leaky path C — client.passages.search() with no tag scope:")
    all_passages = passages.search()
    if any(tok in (p.text or "") for p in all_passages):
        details.append("  LEAK passages.search() (no tags) scanned across tenants")
        print("  LEAK unscoped archival search saw acme's passage")
        leaked = True

    # ── Summary ──
    print()
    print("[letta] Isolation model: CONVENTION — developer-passed identity/project/tags")
    print("  The correct read passes scope; the leak omits it. One argument apart.")
    print("  Per-USER key isolation exists only in Letta Cloud (per-project keys), not OSS.")
    print("  vs. aisotropy: ctx-propagated, per-statement/key scoping — leaky path unrepresentable.")

    return not leaked, details


def live_probe() -> None:
    """If a server is configured, attempt a REAL read-only ping. Never faked."""
    cfg = load_config()
    if not cfg.is_live:
        print("[letta] LIVE server: not configured (LETTA_BASE_URL unset) — offline run.")
        return
    from orbit.client import ping
    try:
        ok = ping(cfg)
        print(f"[letta] LIVE server ping ({cfg.base_url}): {'OK' if ok else 'no'}")
    except Exception as exc:  # noqa: BLE001 - report honestly, do not fake
        print(f"[letta] LIVE server ping FAILED ({cfg.base_url}): {exc!r}")


if __name__ == "__main__":
    print("=" * 64)
    print("Letta (MemGPT) — Adversarial Isolation Smoke Test (real SDK types)")
    print("=" * 64)
    print()

    passed, details = run_matrix()

    print()
    live_probe()

    print()
    print("-" * 64)
    for d in details:
        print(d)
    print("-" * 64)

    if passed:
        print("\nResult: no leak on the tested paths this run")
    else:
        print("\nResult: LEAK CONFIRMED — cross-tenant reads via developer-passed scope")
        print("Root cause: multi-tenancy is convention (identity/project/tags),")
        print("            not framework-enforced. Wrong/absent scope = cross read.")

    # Exit-code contract for this arena's smoke:
    #   The positive control MUST hold (harness healthy) and the leak is the
    #   EXPECTED finding. So exit 0 whenever the matrix ran end-to-end; exit 1
    #   only if the positive control broke (i.e. `details` is empty / harness
    #   failure), which `run_matrix` signals by returning early.
    harness_ok = bool(details) and any("Positive control: acme saw" in d for d in details)
    sys.exit(0 if harness_ok else 1)
