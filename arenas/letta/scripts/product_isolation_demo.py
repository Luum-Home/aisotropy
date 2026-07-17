#!/usr/bin/env python3
"""
Multi-PRODUCT scoping demo for Letta (MemGPT) — the THIRD isolation axis, honestly.

aisotropy scopes every memory read/write by FOUR keys — (tenant_id, user_id,
session_id, product_id) — bound once at the Huma handler boundary and propagated
via context.Context, so an unscoped read is *unrepresentable* by construction.
`test_isolation.py` already proves Letta leaks the first two axes (tenant, user)
when a developer under-scopes the ORG-WIDE primitives. This demo asks the honest
follow-up for a third axis (product): what does a *competent* Letta developer do,
and what genuine sharp edges remain?

The honest answer — grounded in the real `letta-client==1.12.1` surface:

  Letta's DOCUMENTED multi-tenant pattern is ONE AGENT PER PRINCIPAL. Give each
  (tenant, user, product) its own agent and read through the AGENT-SCOPED
  subresources — `client.agents.blocks.list(agent_id)` and
  `client.agents.passages.search(agent_id, query=...)`. Those require an agent_id
  (the SDK rejects an empty one) and return ONLY that agent's memory. Isolation is
  by agent_id, by construction — no `product:<id>` tag, and nothing to forget.

So a competent developer AVOIDS three of the four sharp edges this arena's earlier
draft dressed up as "cross-product leaks": there is no need to fold product into a
tag, no need to rely on `match_all_tags`, and the agent-scoped archival search
cannot span products. What genuinely REMAINS is narrower and real:

  * OSS Letta is a SINGLE ORG: the org-wide primitives (`client.blocks.list()`,
    `client.blocks.retrieve(id)`, `client.passages.search()` with no agent_id)
    carry NO framework binding between the caller and a tenant/product. If a dev
    uses those instead of the agent-scoped reads, nothing stops a cross-read.
  * `blocks.retrieve(id)` performs no ownership check — but it only bites a caller
    who already holds a foreign block id (a row-PK-without-row-level-security
    shape, not an ambient org-wide scan).
  * IF you choose to lean on the tag convention on the org-wide surface, the SDK
    default `match_all_tags=False` (ANY-tag / OR) is an UNSAFE DEFAULT: a query
    that shares the tenant/user tags matches a sibling's block even when you also
    pass a distinct `product:<id>` tag. That is a DX footgun one parameter from
    correct (`match_all_tags=True`), not a structural cross-product leak.

Everything runs OFFLINE against the REAL Letta SDK types (BlockResponse / Passage),
reusing this arena's orbit.memory store, whose accessors mirror the real server's
two surfaces (org-wide top-level + agent-scoped subresource) exactly.
"""

from __future__ import annotations

import os
import secrets
import sys
from dataclasses import dataclass, field

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

from orbit.memory import BlockStore, PassageStore, new_agent_id  # noqa: E402


@dataclass(frozen=True)
class Principal:
    """One (tenant, user, product) principal.

    Letta's documented multi-tenant pattern gives each principal its OWN agent.
    `agent_id` is then the isolation key for the agent-scoped subresources — no
    product tag required. `project_id` (tenant) and the tag convention only matter
    if you fall back to the ORG-WIDE primitives.
    """

    tenant_id: str
    user_id: str
    product_id: str
    agent_id: str = field(default_factory=new_agent_id)

    @property
    def project_id(self) -> str:
        return f"proj-{self.tenant_id}"

    @property
    def product_tag(self) -> str:
        return f"product:{self.product_id}"

    @property
    def tenant_user_tags(self) -> list[str]:
        # What a developer using ONLY Letta's org-wide tenant/user axes would pass.
        return [f"tenant:{self.tenant_id}", f"user:{self.user_id}"]

    @property
    def full_tags(self) -> list[str]:
        # The full three-axis tag scope — only meaningful on the org-wide surface,
        # and only if the dev remembers product AND match_all_tags=True.
        return [*self.tenant_user_tags, self.product_tag]


def marker() -> str:
    return f"lt-{secrets.token_hex(8)}"


def run_matrix() -> tuple[bool, list[str]]:
    details: list[str] = []
    problem = False

    blocks = BlockStore()
    passages = PassageStore()

    # Same tenant + same user, TWO products, each with its OWN agent — the
    # documented Letta multi-tenant shape. This is the exact aisotropy scenario:
    # one operator (acme/alice) running two products that must not see each other.
    checkout = Principal(tenant_id="acme", user_id="alice", product_id="checkout")
    analytics = Principal(tenant_id="acme", user_id="alice", product_id="analytics")

    tok = marker()
    print(f"[letta] Seeding principal (acme/alice/checkout, agent={checkout.agent_id[:14]}…) "
          f"marker: {tok}")

    # ── Seed product 'checkout' on ITS OWN agent (agent-scoped) + also on the
    #    org-wide table (so the org-wide footguns below have something to find). ──
    seeded = blocks.create(
        label="human", value=f"Checkout PII, note: {tok}",
        project_id=checkout.project_id, tags=checkout.full_tags,
        agent_id=checkout.agent_id,
    )
    passages.insert(text=f"Checkout ticket: {tok}", tags=checkout.full_tags,
                    agent_id=checkout.agent_id)
    assert isinstance(seeded, BlockResponse)

    # ── 1. Positive control: checkout's agent reads its own core memory ──
    own = blocks.agent_blocks_list(checkout.agent_id)
    if any(tok in b.value for b in own):
        details.append("  OK  Positive control: checkout's agent saw its own marker")
        print("  OK  Positive control: checkout's agent saw its own marker")
    else:
        details.append("  FAIL Positive control failed — aborting.")
        print("  FAIL Positive control failed — aborting.")
        return False, details

    # ── 2. STRONGEST CONFIG (what a competent dev does): analytics' agent reads
    #    its own scope via the agent-scoped subresources. Isolated by agent_id,
    #    by construction — no product tag involved. Expect NO checkout marker. ──
    print()
    print("[letta] Strongest config — one agent per principal, agent-scoped reads:")
    scoped_blocks = blocks.agent_blocks_list(analytics.agent_id)
    scoped_arch = passages.agent_search(analytics.agent_id, query=None)
    if any(tok in b.value for b in scoped_blocks) or any(
            tok in (p.text or "") for p in scoped_arch):
        details.append(
            "  FAIL agent-scoped read leaked across products — unexpected")
        print("  FAIL agent-scoped read leaked (unexpected)")
        problem = True
    else:
        details.append(
            "  OK  agents.blocks.list(agent_id) + agents.passages.search(agent_id) "
            "returned NOTHING from checkout — isolated by agent_id, by construction")
        print("  OK  agent-scoped reads isolate: checkout invisible to analytics' "
              "agent (no product tag needed)")

    # A competent developer STOPS here — the product boundary already holds. The
    # remaining probes show what breaks ONLY IF you drop to the org-wide surface.
    print()
    print("[letta] Residual sharp edges — ONLY if you use the ORG-WIDE primitives")
    print("        instead of the agent-scoped reads above:")

    # ── 3. Org-wide, no caller binding: single-org top-level list. A request
    #    serving 'analytics' that scopes by tenant/user tags (there is no product
    #    filter on the org-wide surface) reads checkout's block. This is the SAME
    #    single-org finding test_isolation.py already proves for tenant/user — it
    #    is NOT a distinct product mechanism. ──
    as_tenant_user = blocks.list(
        project_id=analytics.project_id,        # same tenant → same project
        tags=analytics.tenant_user_tags,        # same user; no product filter exists here
        match_all_tags=True,
    )
    if any(tok in b.value for b in as_tenant_user):
        details.append(
            "  NOTE org-wide blocks.list(tenant/user) has no caller binding — it "
            "returns checkout's block to an analytics code path (single-org)")
        print("  NOTE org-wide blocks.list is single-org: no product filter, no "
              "caller binding (same finding as tenant/user)")

    # ── 4. UNSAFE DEFAULT footgun: on the org-wide surface, the SDK default
    #    match_all_tags=False (ANY-tag / OR) matches on the shared tenant/user tags
    #    even when the caller ALSO passes its own product:analytics tag. One
    #    parameter from correct (match_all_tags=True), not a structural leak. ──
    print("[letta] Footgun — org-wide blocks.list default match_all_tags=False "
          "(ANY-tag / OR):")
    wrong_default = blocks.list(
        project_id=analytics.project_id,
        tags=analytics.full_tags,               # includes product:analytics
        match_all_tags=False,                   # <-- SDK default is ANY-tag
    )
    correct_default = blocks.list(
        project_id=analytics.project_id,
        tags=analytics.full_tags,
        match_all_tags=True,                    # <-- the one-parameter fix
    )
    if any(tok in b.value for b in wrong_default) and not any(
            tok in b.value for b in correct_default):
        details.append(
            "  FOOTGUN default match_all_tags=False matched checkout via the shared "
            "tenant/user tags; match_all_tags=True fixes it (one parameter, DX trap)")
        print("  FOOTGUN ANY-tag default matched checkout; match_all_tags=True is clean")

    # ── 5. GENUINE single-org primitive: blocks.retrieve(id) performs no ownership
    #    check. But it only bites a caller who already HOLDS a foreign block id —
    #    a row-PK-without-row-level-security shape, not an ambient scan. ──
    print("[letta] Genuine — client.blocks.retrieve(id) with a foreign id (no ownership check):")
    grabbed = blocks.retrieve(seeded.id)
    if grabbed is not None and tok in grabbed.value:
        details.append(
            "  GENUINE blocks.retrieve(id) applied no ownership check — a caller "
            "already holding checkout's id reads it (row PK, no row-level security)")
        print("  GENUINE retrieve-by-id returned checkout's block (needs a foreign id first)")

    # ── Summary ──
    print()
    print("[letta] Product scoping model: use ONE AGENT PER PRINCIPAL.")
    print("  agents.blocks.list(agent_id) / agents.passages.search(agent_id) isolate by")
    print("  agent_id, by construction — a competent dev needs no product tag and avoids")
    print("  the org-wide footguns. What remains is Letta's real single-org posture: the")
    print("  top-level primitives have no caller↔tenant binding, so the app must map")
    print("  verified caller -> agent_id itself.")
    print("  vs. aisotropy: product_id is one of four ctx-propagated scope keys bound at")
    print("       the boundary — enforced per-statement, the cross-product read is")
    print("       unrepresentable and there is no org-wide primitive to fall back to.")

    return not problem, details


if __name__ == "__main__":
    print("=" * 64)
    print("Letta (MemGPT) — Multi-PRODUCT Scoping Demo (3rd axis, real SDK types)")
    print("=" * 64)
    print()

    passed, details = run_matrix()

    print()
    print("-" * 64)
    for d in details:
        print(d)
    print("-" * 64)

    if passed:
        print("\nResult: agent-scoped reads isolate by construction (strongest config "
              "holds); org-wide primitives remain single-org with the noted footguns")
    else:
        print("\nResult: UNEXPECTED — agent-scoped isolation did not hold; investigate")

    # Exit-code contract (same spirit as smoke.py): the positive control MUST hold
    # (harness healthy) and the agent-scoped isolation MUST hold (the honest
    # finding). Exit 0 when the matrix ran end-to-end with both true; exit 1 only
    # if the positive control broke.
    harness_ok = bool(details) and any(
        "Positive control: checkout's agent saw" in d for d in details)
    sys.exit(0 if harness_ok and passed else 1)
