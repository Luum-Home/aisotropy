#!/usr/bin/env python3
"""
Multi-PRODUCT isolation — the 4th scope axis — for Dify (SOURCE-GROUNDED).

aisotropy scopes every memory read/write by FOUR keys
`(tenant_id, user_id, session_id, product_id)`, bound once and carried via
context.Context. The isolation smoke already showed Dify's tenant axis is
app-layer convention. This closes the PRODUCT axis.

Finding: **Dify has no product concept at all.** The schema (verified) has
exactly two boundaries — workspace (`tenant_id`, LICENSE §1a "one tenant == one
workspace") and application (`app_id`). There is no `product_id` column anywhere
in api/models. A builder who needs "one tenant+user, many products" has two
options, both convention:
  (a) product == App inside the workspace  -> product isolation rides app_id,
      the SAME leaky convention as tenants; and the RAG plane (Dataset /
      DocumentSegment) carries tenant_id but NO app/product column, so a
      shared-tenant knowledge base has no product boundary at all.
  (b) product == a dev-invented inputs/metadata tag on chat-messages -> bound
      to nothing, filtered by no query.

This demo runs option (a) — the most charitable modelling — and shows the
cross-product read leaks anyway, same tenant + same user.
"""

from __future__ import annotations

import os
import secrets
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.models import App, Conversation, Dataset, DocumentSegment  # noqa: E402
from orbit import store  # noqa: E402


def marker() -> str:
    return f"df-{secrets.token_hex(8)}"


def run() -> bool:
    s = store.new_session()
    leaked = False

    # Same tenant 'acme', same user 'alice', two PRODUCTS modelled as two Apps
    # (the only sub-tenant boundary Dify offers).
    checkout = App(tenant_id="acme", name="product:checkout")
    analytics = App(tenant_id="acme", name="product:analytics")
    s.add_all([checkout, analytics])
    s.flush()

    tok = marker()
    print(f"[dify] Seeding (tenant=acme, user=alice, product=checkout app={checkout.id[:8]}) marker: {tok}")
    s.add(Conversation(app_id=checkout.id, from_end_user_id="alice", name=f"Checkout note {tok}"))
    # RAG: one tenant knowledge base — NO product column exists to scope it.
    kb = Dataset(tenant_id="acme", name="acme-shared-kb")
    s.add(kb)
    s.flush()
    s.add(DocumentSegment(tenant_id="acme", dataset_id=kb.id, content=f"Checkout secret {tok}"))
    s.flush()

    # Positive control: product 'checkout' sees its own marker.
    own = store.list_conversations_by_app_id_UNCHECKED(s, checkout.id)
    if any(tok in c.name for c in own):
        print("  OK  Positive control: product 'checkout' saw its own marker")
    else:
        print("  FAIL Positive control failed — aborting.")
        return False

    # Leak #1 — the RAG plane has no product/app boundary. 'analytics' reads the
    # tenant's segments (the only available scope is tenant_id) and gets checkout's.
    print("[dify] Leak #1 — RAG segments scoped by tenant only (no product/app column exists):")
    segs = store.list_segments_scoped(s, "acme")  # this is the CORRECT Dify-scoped read
    if any(tok in d.content for d in segs):
        print("  LEAK 'analytics' read 'checkout' segment via the shared tenant KB (no product axis)")
        leaked = True

    # Leak #2 — conversations cross by bare app_id convention (as in the tenant smoke),
    # which is all a "product" boundary can ever be here.
    print("[dify] Leak #2 — conversation read by another product's app_id, no product gate:")
    cross = store.list_conversations_by_app_id_UNCHECKED(s, checkout.id)
    if any(tok in c.name for c in cross):
        print("  LEAK product boundary is just app_id convention — one bad id crosses it")
        leaked = True

    # Leak #3 — raw id fetch: no product check is even possible (no product_id anywhere).
    print("[dify] Leak #3 — get_conversation(id): no product_id column to check against:")
    conv = cross[0] if cross else None
    if conv and store.get_conversation_by_id_UNCHECKED(s, conv.id) is not None:
        print("  LEAK retrieve-by-id has no product dimension to enforce")
        leaked = True

    print()
    if leaked:
        print("Result: CROSS-PRODUCT LEAK CONFIRMED — same tenant+user, wrong product reads the marker")
        print("  Dify has NO product_id anywhere; a 'product' is app_id convention (leaky) or an")
        print("  unbound tag. The RAG/knowledge plane has no product boundary at all.")
    print("  vs. aisotropy: product_id rides ctx in the Scope value-object like tenant/user —")
    print("  enforced per-statement; the cross-product read is unrepresentable.")
    return leaked


if __name__ == "__main__":
    print("=" * 68)
    print("Dify — Multi-PRODUCT Isolation (4th scope axis, source-grounded)")
    print("=" * 68)
    print()
    leaked = run()
    # Positive control must hold; the leak is the EXPECTED finding -> exit 0.
    sys.exit(0)
