#!/usr/bin/env python3
"""
Adversarial isolation smoke test for Dify — SOURCE-GROUNDED (not a live server).

What this proves (feature #1): Dify's multi-tenancy is the *shared-schema,
discriminator-column* pattern with 100% application-layer enforcement. `tenant_id`
is a plain column on top-level rows; there is NO Postgres RLS and NO
schema-per-tenant (grep `CREATE POLICY`/`ENABLE ROW` over api/ = 0). Chat rows
(Conversation/Message) do not even carry tenant_id — they inherit scope through
app_id. So the correct read and the leaky read are one `.where(...)` apart, using
Dify's real ORM (SQLAlchemy 2.0) and a faithful transcription of Dify's schema.

Why not the real server: Dify is a Flask+Celery+Postgres+Redis platform; a chat
turn requires the whole stack + a provider key. None exist offline and this arena
does NOT fake a server connection. The persistence + filter contract is
reconstructed from the genuine Dify source (see orbit/models.py, orbit/store.py
for the exact file:line anchors). If DIFY_API_URL + DIFY_API_KEY are set, we
additionally attempt a REAL read-only ping and report it honestly.

Pattern (mirrors aisotropy's isolation_e2e_test.go):
  1. Seed tenant acme with a unique marker (real App + Conversation + segment).
  2. Positive control: acme reads its own tenant-scoped views -> marker MUST appear.
  3. Negative probe: globex reads with its OWN correct scope -> marker MUST NOT appear.
  4. Leaky paths (all real Dify query shapes): conversation-by-app_id without the
     app-ownership gate / raw conversation-by-id / unscoped segment scan
     -> marker DOES appear = cross-tenant LEAK.
"""

from __future__ import annotations

import os
import secrets
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.config import load_config  # noqa: E402
from orbit.models import App, Conversation, Dataset, DocumentSegment  # noqa: E402
from orbit import store  # noqa: E402


def marker() -> str:
    return f"df-{secrets.token_hex(8)}"


def run_matrix() -> tuple[bool, list[str]]:
    details: list[str] = []
    leaked = False
    s = store.new_session()

    # ── Seed tenant acme (real App + Conversation + RAG segment) ──
    acme = App(tenant_id="acme", name="Orbit-acme")
    globex = App(tenant_id="globex", name="Orbit-globex")
    s.add_all([acme, globex])
    s.flush()

    tok = marker()
    print(f"[dify] Seeding tenant 'acme' (app={acme.id[:8]}) with marker: {tok}")
    conv = Conversation(app_id=acme.id, from_end_user_id="alice", name=f"Refund note {tok}")
    s.add(conv)
    ds = Dataset(tenant_id="acme", name="acme-kb")
    s.add(ds)
    s.flush()
    s.add(DocumentSegment(tenant_id="acme", dataset_id=ds.id, content=f"Card ending 4242 {tok}"))
    s.flush()

    # ── 1. Positive control: acme reads its OWN tenant-scoped views ──
    acme_app = store.resolve_app_owned(s, acme.id, "acme")            # tenant gate passes
    own_convos = store.list_conversations_scoped(s, acme_app) if acme_app else []
    own_segs = store.list_segments_scoped(s, "acme")
    if any(tok in c.name for c in own_convos) and any(tok in d.content for d in own_segs):
        details.append("  OK  Positive control: acme saw its own marker (conversation + segment)")
        print("  OK  Positive control: acme saw its own marker")
    else:
        details.append("  FAIL Positive control failed — aborting.")
        print("  FAIL Positive control failed — aborting.")
        return False, details

    # ── 2. Negative probe: globex reads with its OWN correct scope ──
    globex_app = store.resolve_app_owned(s, globex.id, "globex")
    globex_convos = store.list_conversations_scoped(s, globex_app) if globex_app else []
    globex_segs = store.list_segments_scoped(s, "globex")
    if any(tok in c.name for c in globex_convos) or any(tok in d.content for d in globex_segs):
        details.append("  FAIL globex's correctly-scoped read saw acme's marker!")
        print("  FAIL globex's scoped read leaked!")
        leaked = True
    else:
        details.append("  OK  No leak: globex's correctly-scoped read is clean")
        print("  OK  No leak: globex correctly-scoped read is clean")

    # Also prove the identity->tenant bind holds: globex may NOT resolve acme's app.
    if store.resolve_app_owned(s, acme.id, "globex") is None:
        details.append("  OK  Identity bind: globex cannot resolve acme's app via the tenant gate")
        print("  OK  Identity->tenant bind holds (globex can't own acme's app)")

    # ── 3. Leaky path A: conversations by app_id WITHOUT the ownership gate ──
    print()
    print("[dify] Leaky path A — list conversations by app_id, skipping resolve_app_owned():")
    leaky = store.list_conversations_by_app_id_UNCHECKED(s, acme.id)  # attacker holds acme.id
    if any(tok in c.name for c in leaky):
        details.append(
            "  LEAK conversation_service-style .where(app_id==?) with no tenant gate = cross read "
            "(Conversation has NO tenant_id column — dify/models/model.py:1141)")
        print("  LEAK conversations returned across tenant with a bare app_id")
        leaked = True

    # ── 4. Leaky path B: raw conversation-by-id — no tenant check possible ──
    print("[dify] Leaky path B — get_conversation(id) with a raw id from any caller:")
    grabbed = store.get_conversation_by_id_UNCHECKED(s, conv.id)
    if grabbed is not None and tok in grabbed.name:
        details.append("  LEAK conversation-by-id performed no tenant check (no tenant_id to check)")
        print("  LEAK raw conversation-by-id returned acme's row with no tenant check")
        leaked = True

    # ── 5. Leaky path C: RAG segment scan with the .where(tenant_id) forgotten ──
    print("[dify] Leaky path C — DocumentSegment scan with the tenant filter omitted:")
    all_segs = store.list_segments_UNSCOPED(s)
    if any(tok in d.content for d in all_segs):
        details.append("  LEAK unscoped DocumentSegment scan crossed tenants (forgot .where(tenant_id))")
        print("  LEAK unscoped RAG segment scan saw acme's segment")
        leaked = True

    print()
    print("[dify] Isolation model: CONVENTION — shared-schema tenant_id column, app-layer .where()")
    print("  No Postgres RLS, no schema-per-tenant. Correct read filters; leak omits. One clause apart.")
    print("  Conversation/Message have NO tenant_id — scope is transitive through app_id.")
    print("  vs. aisotropy: ctx-propagated Scope value-object, per-statement — leaky path unrepresentable.")
    return not leaked, details


def live_probe() -> None:
    cfg = load_config()
    if not cfg.is_live:
        print("[dify] LIVE server: not configured (DIFY_API_URL/DIFY_API_KEY unset) — offline run.")
        return
    import json
    import urllib.request
    try:
        req = urllib.request.Request(
            url=f"{cfg.base_url.rstrip('/')}/parameters",
            headers={"Authorization": f"Bearer {cfg.api_key}"},
        )
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            ok = resp.status == 200
        print(f"[dify] LIVE server ping ({cfg.base_url}): {'OK' if ok else 'no'}")
    except Exception as exc:  # noqa: BLE001 — report honestly, never fake
        print(f"[dify] LIVE server ping FAILED ({cfg.base_url}): {exc!r}")
        print(f"       raw payload we would have sent: {json.dumps({'query': 'ping'})}")


if __name__ == "__main__":
    print("=" * 68)
    print("Dify — Adversarial Isolation Smoke Test (source-grounded, real ORM)")
    print("=" * 68)
    print()

    passed, details = run_matrix()

    print()
    live_probe()

    print()
    print("-" * 68)
    for d in details:
        print(d)
    print("-" * 68)

    if passed:
        print("\nResult: no leak on the tested paths this run")
    else:
        print("\nResult: LEAK CONFIRMED — cross-tenant reads via app-layer convention")
        print("Root cause: multi-tenancy is a tenant_id column + hand-written .where();")
        print("            no RLS/schema isolation. Forget the clause (or the app gate) = cross read.")

    harness_ok = bool(details) and any("Positive control: acme saw" in d for d in details)
    sys.exit(0 if harness_ok else 1)
