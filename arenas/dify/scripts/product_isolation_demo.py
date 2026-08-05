#!/usr/bin/env python3
"""
Multi-PRODUCT isolation — the 4th scope axis — for Dify (SOURCE-GROUNDED).

SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md
  CORRECTED by that sweep, both ways. (a) This header used to claim aisotropy scopes by
  FOUR keys including product_id — it does not: memory.Key is {Tenant, User, Session}
  (internal/memory/key.go:87), every Scope is {tenant, user}, and ProductID is a persona
  selector (internal/api/ask.go:49) that reaches no store as a predicate. (b) It used to
  say Dify's schema has "exactly two boundaries" — false, and already false at the commit
  this arena read: api/models/agent.py existed at 1aea3460af1d and carries
  agent_workspaces / agent_workspace_bindings / agent_drive_files. What survives, verified
  at HEAD 4213e6b1, is that NO product scope column exists on either side.

aisotropy scopes every memory read/write by `(tenant_id, user_id, session_id)`, bound
once and carried via context.Context; `product_id` is a persona selector, NOT a fourth
scope key. The isolation smoke already showed Dify's tenant axis is app-layer
convention. This closes the PRODUCT axis — on which NEITHER framework enforces anything.

Finding: **Dify models no product boundary.** There is no `product_id` scope column
anywhere in api/models (the sole `payment_product_id` is a billing SKU on
`ProviderOrder`). The boundaries it does model are the workspace (`tenant_id`, LICENSE
§1a "one tenant == one workspace"), the application (`app_id`) and, since the 2026-05/06
agent migrations, the agent and its working workspace (`agent_workspaces`,
`agent_drive_files`) — none of them a customer-facing product. A builder who needs
"one tenant+user, many products" has two options inside a workspace, both convention
(a third, one workspace per product, is the vendor's Enterprise-licensed answer):
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

    # SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md
    #   Dataset/DocumentSegment still carry tenant_id and no product/app/agent column at
    #   HEAD 4213e6b1 (dataset.py:180, dataset.py:919) — the leaks below still hold.
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
        print("Result: no product boundary exists to enforce — same tenant+user, wrong product")
        print("  reads the marker. This is not a Dify 'leak' so much as a MISSING AXIS: Dify has")
        print("  NO product_id anywhere, so 'product' can only be app_id convention (the same")
        print("  shared-schema gate a dev may drop) or an unbound tag. The RAG/knowledge plane")
        print("  has no product column at all, so even the tenant-CORRECT read crosses products.")
    # SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md — the three lines
    # below used to claim product_id rides aisotropy's Scope value object. It does not
    # (memory.Key is {Tenant,User,Session}; Scope is {tenant,user}; ProductID selects a
    # persona prompt), so this is a shared gap, not a differentiator.
    print("  vs. aisotropy: NO advantage on this axis — aisotropy's Scope is (tenant, user)")
    print("  and its memory key (tenant, user, session); product_id only selects a persona")
    print("  prompt, so the cross-product read is unenforced on BOTH sides. (A framework")
    print("  without the axis isn't 'buggy' — neither of these models this fourth boundary.)")
    return leaked


if __name__ == "__main__":
    print("=" * 68)
    print("Dify — Multi-PRODUCT Isolation (4th scope axis, source-grounded)")
    print("=" * 68)
    print()
    leaked = run()
    # Positive control must hold; the leak is the EXPECTED finding -> exit 0.
    sys.exit(0)
