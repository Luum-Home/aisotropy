#!/usr/bin/env python3
"""
Multi-PRODUCT isolation demo for Letta (MemGPT) — the THIRD isolation axis.

aisotropy scopes every memory read/write by FOUR keys — (tenant_id, user_id,
session_id, product_id) — bound once at the Huma handler boundary and propagated
via context.Context so an unscoped read is *unrepresentable*. The isolation smoke
in this arena already showed the first two axes (tenant, user) leak in Letta
because they are developer-passed conventions. This demo closes the gap on the
THIRD axis: product_id.

The finding, stated plainly: **product_id is just another developer-passed key
the framework never binds to anything.** The real Letta list/search API surface
(`client.blocks.list(project_id=, identity_id=, label=, tags=, match_all_tags=)`,
`client.passages.search(query=, tags=, tag_match_mode=, archive_id=)`) has NO
`product_id` parameter at all. A developer who wants a product boundary has only
one lever: fold `product:<id>` into the `tags` convention and remember to pass it
on EVERY read. Nothing in the framework requires it. So:

  * If a caller scopes by the axes Letta natively understands (project=tenant,
    identity=user) and omits the product tag — the natural mistake, since there
    is no product filter to forget — it reads a DIFFERENT product's data for the
    same tenant+user. That is a CROSS-PRODUCT LEAK.
  * `blocks.retrieve(id)` performs no product check either: a raw id from
    product X is readable from a product-Y request path.

Everything runs OFFLINE against the REAL Letta SDK types (BlockResponse /
Passage), reusing this arena's orbit.memory store, whose list()/search() mirror
the real server's documented, developer-passed filter contract exactly.

Pattern (mirrors aisotropy's isolation_e2e_test.go, extended to the product axis):
  1. Seed (tenant=acme, user=alice, product=checkout) with a unique marker.
  2. Positive control: the SAME (tenant, user, product) reads its own marker.
  3. CROSS-PRODUCT LEAK: a request serving product 'analytics' — same tenant acme,
     same user alice — reads checkout's marker, because product_id binds to
     nothing. Shown three ways (Letta-native tenant/user scope; wrong product
     tag; raw retrieve-by-id).
"""

from __future__ import annotations

import os
import secrets
import sys
from dataclasses import dataclass

# Import the real SDK up front so a missing dependency fails loudly.
try:
    import letta_client  # noqa: F401
    from letta_client.types import BlockResponse, Passage  # noqa: F401
except ImportError:
    print("ERROR: letta-client not installed. Run: pip install -r requirements.txt",
          file=sys.stderr)
    sys.exit(1)

# Make `orbit` importable when run as `python scripts/product_isolation_demo.py`.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.memory import BlockStore, PassageStore  # noqa: E402


@dataclass(frozen=True)
class ProductScope:
    """The scope a developer *should* attach on all three axes.

    Letta's real API exposes `project_id` (tenant) and `identity_id` (user) as
    native filters. It exposes NO product_id filter — so the only place a product
    boundary can live is the developer-passed `tags` convention. This value object
    makes visible that the product axis is bolted on by convention, not bound by
    the framework: `product_tag` is one more optional string on `tags`.
    """

    tenant_id: str
    user_id: str
    product_id: str

    @property
    def project_id(self) -> str:
        return f"proj-{self.tenant_id}"

    @property
    def product_tag(self) -> str:
        return f"product:{self.product_id}"

    @property
    def tenant_user_tags(self) -> list[str]:
        # What a developer using ONLY Letta's native tenant/user axes would pass.
        # Note: no product here — Letta has no product concept to remind them.
        return [f"tenant:{self.tenant_id}", f"user:{self.user_id}"]

    @property
    def full_tags(self) -> list[str]:
        # The full three-axis scope — only correct if the dev remembers product.
        return [*self.tenant_user_tags, self.product_tag]


def marker() -> str:
    return f"lt-{secrets.token_hex(8)}"


def run_matrix() -> tuple[bool, list[str]]:
    details: list[str] = []
    leaked = False

    blocks = BlockStore()
    passages = PassageStore()

    # Same tenant + same user, TWO products. This is the exact aisotropy shape:
    # one operator (acme/alice) running two products that must not see each other.
    checkout = ProductScope(tenant_id="acme", user_id="alice", product_id="checkout")
    analytics = ProductScope(tenant_id="acme", user_id="alice", product_id="analytics")

    tok = marker()
    print(f"[letta] Seeding (tenant=acme, user=alice, product=checkout) marker: {tok}")

    # ── Seed product 'checkout' (real BlockResponse + Passage), full 3-axis tags ──
    seeded = blocks.create(
        label="human", value=f"Checkout PII, note: {tok}",
        project_id=checkout.project_id, tags=checkout.full_tags,
    )
    passages.insert(text=f"Checkout ticket: {tok}", tags=checkout.full_tags)
    assert isinstance(seeded, BlockResponse)

    # ── 1. Positive control: SAME (tenant, user, product) reads its own marker ──
    own = blocks.list(project_id=checkout.project_id, tags=checkout.full_tags,
                      match_all_tags=True)
    if any(tok in b.value for b in own):
        details.append("  OK  Positive control: product 'checkout' saw its own marker")
        print("  OK  Positive control: product 'checkout' saw its own marker")
    else:
        details.append("  FAIL Positive control failed — aborting.")
        print("  FAIL Positive control failed — aborting.")
        return False, details

    # ── 2. CROSS-PRODUCT LEAK #1 — Letta-native tenant/user scope (no product) ──
    # A request serving product 'analytics' scopes by the axes Letta actually
    # supports: project_id (tenant) + the tenant/user tags. There is NO product
    # filter to pass, so 'analytics' reads 'checkout' data for the same tenant+user.
    print()
    print("[letta] Leak #1 — product 'analytics' scopes by tenant+user "
          "(Letta has no product filter):")
    as_tenant_user = blocks.list(
        project_id=analytics.project_id,        # same tenant → same project
        tags=analytics.tenant_user_tags,        # same user; product simply absent
        match_all_tags=True,
    )
    if any(tok in b.value for b in as_tenant_user):
        details.append(
            "  LEAK product 'analytics' read product 'checkout' marker via the "
            "tenant/user scope — product_id is not a bound dimension")
        print("  LEAK 'analytics' saw 'checkout' marker (product axis not enforced)")
        leaked = True

    # ── 3. CROSS-PRODUCT LEAK #2 — WRONG product tag still bleeds without match_all ──
    # A dev who DOES tag product but uses the default OR tag-match (match_all_tags
    # =False, the real API default) while querying with analytics' own tags still
    # matches checkout's block on the shared tenant/user tags.
    print("[letta] Leak #2 — 'analytics' passes its OWN full tags but default "
          "OR match (real API default):")
    wrong_product = blocks.list(
        project_id=analytics.project_id,
        tags=analytics.full_tags,               # includes product:analytics
        match_all_tags=False,                   # <-- real Letta default is ANY-tag
    )
    if any(tok in b.value for b in wrong_product):
        details.append(
            "  LEAK default ANY-tag match: sharing tenant/user tags is enough — "
            "checkout's block matched an analytics query (match_all_tags=False default)")
        print("  LEAK ANY-tag default matched checkout via shared tenant/user tags")
        leaked = True

    # ── 4. CROSS-PRODUCT LEAK #3 — retrieve(id) performs no product check ──
    print("[letta] Leak #3 — client.blocks.retrieve(id) from a product-'analytics' path:")
    grabbed = blocks.retrieve(seeded.id)
    if grabbed is not None and tok in grabbed.value:
        details.append(
            "  LEAK blocks.retrieve(id) applied no product check — a checkout id "
            "is readable from an analytics request path")
        print("  LEAK retrieve-by-id returned checkout's block, no product check")
        leaked = True

    # ── 5. CROSS-PRODUCT LEAK #4 — archival search, tenant/user tags only ──
    print("[letta] Leak #4 — passages.search() scoped by tenant/user (no product):")
    hits = passages.search(tags=analytics.tenant_user_tags, tag_match_mode="all")
    if any(tok in (p.text or "") for p in hits):
        details.append(
            "  LEAK archival search on tenant/user tags returned checkout's "
            "passage — product boundary invisible to vector recall too")
        print("  LEAK archival search crossed the product boundary")
        leaked = True

    # ── Summary ──
    print()
    print("[letta] Product isolation model: CONVENTION — product_id binds to NOTHING")
    print("  Letta's real list/search API has project_id + identity_id (tenant/user)")
    print("  but NO product_id filter. The product axis exists only if the developer")
    print("  invents a `product:<id>` tag AND remembers match_all_tags=True on every")
    print("  read. Miss it once and a sibling product reads your data.")
    print("  vs. aisotropy: product_id is one of four ctx-propagated scope keys,")
    print("       enforced per-statement — the cross-product read is unrepresentable.")

    return not leaked, details


if __name__ == "__main__":
    print("=" * 64)
    print("Letta (MemGPT) — Multi-PRODUCT Isolation Demo (3rd axis, real SDK types)")
    print("=" * 64)
    print()

    passed, details = run_matrix()

    print()
    print("-" * 64)
    for d in details:
        print(d)
    print("-" * 64)

    if passed:
        print("\nResult: no cross-product leak on the tested paths this run")
    else:
        print("\nResult: CROSS-PRODUCT LEAK CONFIRMED — same tenant+user, wrong "
              "product reads the marker")
        print("Root cause: product_id is a developer-passed key with no framework")
        print("            binding. Letta's API has no product filter; the product")
        print("            axis is a tag convention, unenforced by construction.")

    # Exit-code contract (same as smoke.py): the positive control MUST hold
    # (harness healthy); the cross-product LEAK is the EXPECTED finding. Exit 0
    # whenever the matrix ran end-to-end; exit 1 only if the positive control broke.
    harness_ok = bool(details) and any(
        "Positive control: product 'checkout' saw" in d for d in details)
    sys.exit(0 if harness_ok else 1)
