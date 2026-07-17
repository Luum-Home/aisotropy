#!/usr/bin/env python3
"""
Dify STRENGTHS demo — steelman, SOURCE-VERIFIED (langgenius/dify @ 1aea3460).

The isolation smokes in this arena are adversarial by design. This demo is the
other half of an honest comparison: what Dify is **genuinely great at**. Dify is
the broadest *product* in the arena set — a batteries-included low-code LLM app
platform — and that breadth is a real, differentiated strength that aisotropy (a
library/runtime, not a product) does NOT try to match.

Honesty discipline (this is a labelled source analysis, not a live server):
  - Capabilities that can be exercised against the transcribed ORM / real payload
    builder offline are RUN-VERIFIED here (the membership-checked identity bind, the
    conversation-history + per-dataset RAG data planes, the real /v1/chat-messages
    body).
  - Capabilities that are inherently server/UI-backed (visual app builder, workflow
    orchestration, plugin ecosystem, model management, ~15 vector backends) are
    DESCRIBED precisely from the real source and labelled NOT-RUN — no fake server,
    no fabricated output.

Strengths shown:
  1. Membership-checked identity->tenant bind       — RUN-VERIFIED (offline ORM)
  2. Real product data planes (chat history + RAG)  — RUN-VERIFIED (offline ORM)
  3. Genuine /v1/chat-messages streaming request     — RUN-VERIFIED (payload builder)
  4. Batteries-included product surface             — DESCRIBED (server/UI-backed)
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.models import App, Conversation, Dataset, DocumentSegment, Message  # noqa: E402
from orbit import store  # noqa: E402
from orbit.agent import build_chat_payload  # noqa: E402


def strength_membership_bind() -> tuple[bool, list[str]]:
    """1. Membership-checked identity->tenant bind — RUN-VERIFIED.

    Unlike frameworks whose dev-passed filters bind to nothing, Dify's
    `Account.set_tenant_id_with_session` (account.py:166) verifies a
    `TenantAccountJoin` membership row before it will let an account act as a
    workspace. You cannot switch into a workspace you do not belong to. This is a
    real, structurally-enforced identity bind — genuinely stronger than most
    arenas — and it is executable against the transcribed ORM here.
    """
    notes: list[str] = []
    s = store.new_session()
    from orbit.models import Account, Tenant, TenantAccountJoin

    acme = Tenant(id="acme", name="Acme")
    globex = Tenant(id="globex", name="Globex")
    alice = Account(id="alice", name="Alice")
    s.add_all([acme, globex, alice,
               TenantAccountJoin(tenant_id="acme", account_id="alice")])
    s.flush()

    print("[dify] Membership-checked identity->tenant bind (account.py:166)")
    member = store.resolve_current_tenant(s, "alice", "acme")
    nonmember = store.resolve_current_tenant(s, "alice", "globex")
    print(f"  alice acting as acme   (is member)   -> {member!r}")
    print(f"  alice acting as globex (not a member) -> {nonmember!r}")
    if member == "acme" and nonmember is None:
        notes.append("  OK  membership row REQUIRED before an account may act as a tenant "
                     "(genuine bind, run-verified)")
        return True, notes
    notes.append("  FAIL membership bind did not behave as documented")
    return False, notes


def strength_product_data_planes() -> tuple[bool, list[str]]:
    """2. Real product data planes — RUN-VERIFIED.

    Dify ships two durable, server-side data planes out of the box: conversation
    history (Conversation/Message in Postgres) and per-dataset RAG knowledge
    (Dataset/DocumentSegment + a per-dataset vector collection). These are product
    features you would otherwise hand-build. We seed and read them (correctly
    scoped) against the transcribed ORM to show the planes are real and functional.
    """
    notes: list[str] = []
    s = store.new_session()
    app = App(tenant_id="acme", name="Orbit")
    s.add(app)
    s.flush()
    conv = Conversation(app_id=app.id, from_end_user_id="alice", name="Order help")
    s.add(conv)
    s.flush()
    s.add(Message(app_id=app.id, conversation_id=conv.id,
                  query="where is my order?", answer="It shipped Tuesday."))
    ds = Dataset(tenant_id="acme", name="acme-kb")
    s.add(ds)
    s.flush()
    s.add(DocumentSegment(tenant_id="acme", dataset_id=ds.id,
                          content="Orders ship within 2 business days."))
    s.flush()

    print("[dify] Product data planes — conversation history + per-dataset RAG")
    app_owned = store.resolve_app_owned(s, app.id, "acme")
    convos = store.list_conversations_scoped(s, app_owned) if app_owned else []
    segs = store.list_segments_scoped(s, "acme")
    print(f"  conversation-history plane: {len(convos)} conversation(s) for the app")
    print(f"  RAG/knowledge plane       : {len(segs)} segment(s) for the tenant")
    if convos and segs:
        notes.append("  OK  both durable data planes present and correctly-scoped-readable "
                     "(chat history + knowledge RAG, run-verified)")
        return True, notes
    notes.append("  FAIL a data plane did not return its seeded row")
    return False, notes


def strength_streaming_payload() -> tuple[bool, list[str]]:
    """3. Genuine /v1/chat-messages streaming request — RUN-VERIFIED.

    Dify's REST contract is real and stable: `POST /v1/chat-messages` with
    `response_mode="streaming"` yields an SSE stream, plus a stop-generation
    endpoint. We build the genuine request body here (every key is a real Dify
    parameter) — the live POST needs a running server + provider key (NOT-RUN).
    """
    notes: list[str] = []
    payload = build_chat_payload(query="Where is my refund?", user="end-user-42",
                                 conversation_id=None)
    print("[dify] Real /v1/chat-messages streaming request body")
    print(f"  payload keys: {sorted(payload)}")
    ok = payload.get("response_mode") == "streaming" and payload.get("query") and "user" in payload
    if ok:
        notes.append("  OK  genuine streaming request body built (real Dify keys: "
                     "inputs/query/response_mode/user); live POST is NOT-RUN (needs a server)")
        return True, notes
    notes.append("  FAIL payload was not the real streaming shape")
    return False, notes


def strength_product_surface() -> list[str]:
    """4. Batteries-included product surface — DESCRIBED (server/UI-backed, NOT-RUN).

    This is Dify's headline strength and it is NOT executable in an offline,
    server-less env — so it is described precisely from the real source, not faked.
    All are genuine product features aisotropy does not ship (aisotropy is a library
    + runtime, not an app platform):
      - Visual low-code app builder + published chat/agent/workflow apps (web console)
      - Workflow orchestration (DAG of LLM / tool / retrieval / code nodes, per-node retry)
      - Per-dataset RAG knowledge bases across ~15 vector backends (core/rag, vector_factory)
      - Agent apps: function-calling + ReAct, built-in + OpenAPI tools + MCP (core/tools, core/mcp)
      - Model runtime / provider registry spanning dozens of vendors, swappable per node
      - Per-message cost with a real USD field (Message.total_price + currency) + moderation
      - A large, growing plugin ecosystem (tools/models/extensions)
    """
    notes: list[str] = []
    surface = [
        "visual low-code app builder (web console)",
        "workflow orchestration (node DAG + per-node retry)",
        "per-dataset RAG over ~15 vector backends (vector_factory)",
        "agent apps: function-calling/ReAct + built-in/OpenAPI tools + MCP",
        "provider registry (dozens of model vendors, swappable)",
        "per-message USD cost field + input/output moderation",
        "plugin ecosystem (tools/models/extensions)",
    ]
    print("[dify] Batteries-included product surface (DESCRIBED, server/UI-backed):")
    for item in surface:
        print(f"  - {item}")
    notes.append(f"  DESCRIBED {len(surface)} product-surface capabilities (source-verified, "
                 "NOT-RUN — need the full platform)")
    return notes


def main() -> int:
    print("=" * 68)
    print("Dify — Strengths Demo (steelman, source-verified @ 1aea3460)")
    print("=" * 68)
    print()

    details: list[str] = []
    all_ok = True

    ok1, n = strength_membership_bind()
    details += n
    all_ok = all_ok and ok1
    print()
    ok2, n = strength_product_data_planes()
    details += n
    all_ok = all_ok and ok2
    print()
    ok3, n = strength_streaming_payload()
    details += n
    all_ok = all_ok and ok3
    print()
    details += strength_product_surface()

    print()
    print("-" * 68)
    for d in details:
        print(d)
    print("-" * 68)
    print()
    print("[dify] These are genuine, differentiated PRODUCT strengths. Dify is the")
    print("  broadest app platform in the arena set — a complete product, not a library.")
    print("  aisotropy does NOT compete on product breadth/UX: it is an embeddable")
    print("  isolation-by-construction runtime. Where aisotropy differs: it is")
    print("  self-hostable for MULTI-TENANT use with no license blocker, and the")
    print("  unscoped cross-read is unrepresentable rather than merely avoidable.")

    # Exit 0 when the run-verified strengths hold (bind + data planes + payload).
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())
