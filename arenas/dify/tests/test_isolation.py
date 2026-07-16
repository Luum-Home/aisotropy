"""
Isolation asserted as executable tests over Dify's real ORM + faithful schema.
These encode the smoke as pass/fail invariants:
  - the CORRECT scoped read isolates (positive control + clean negative probe);
  - the identity->tenant bind holds (membership-checked, like Dify's real one);
  - the LEAKY read crosses tenants (the finding: convention, not enforcement).
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import pytest  # noqa: E402

from orbit.models import App, Conversation, Dataset, DocumentSegment  # noqa: E402
from orbit import store  # noqa: E402


@pytest.fixture()
def seeded():
    s = store.new_session()
    acme = App(tenant_id="acme", name="Orbit-acme")
    globex = App(tenant_id="globex", name="Orbit-globex")
    s.add_all([acme, globex])
    s.add_all([
        __import__("orbit.models", fromlist=["TenantAccountJoin"]).TenantAccountJoin(
            tenant_id="acme", account_id="alice"),
    ])
    s.flush()
    conv = Conversation(app_id=acme.id, from_end_user_id="alice", name="acme SECRET-42")
    s.add(conv)
    ds = Dataset(tenant_id="acme", name="acme-kb")
    s.add(ds)
    s.flush()
    s.add(DocumentSegment(tenant_id="acme", dataset_id=ds.id, content="segment SECRET-42"))
    s.flush()
    return s, acme, globex, conv


def test_positive_control_scoped_read_sees_own(seeded):
    s, acme, _, _ = seeded
    app = store.resolve_app_owned(s, acme.id, "acme")
    assert app is not None
    convos = store.list_conversations_scoped(s, app)
    assert any("SECRET-42" in c.name for c in convos)


def test_negative_probe_correct_scope_is_clean(seeded):
    s, _, globex, _ = seeded
    app = store.resolve_app_owned(s, globex.id, "globex")
    convos = store.list_conversations_scoped(s, app) if app else []
    segs = store.list_segments_scoped(s, "globex")
    assert not any("SECRET-42" in c.name for c in convos)
    assert not any("SECRET-42" in d.content for d in segs)


def test_identity_tenant_bind_is_membership_checked(seeded):
    s, acme, _, _ = seeded
    # alice is a member of acme -> resolves.
    assert store.resolve_current_tenant(s, "alice", "acme") == "acme"
    # alice is NOT a member of globex -> None (Dify account.py:166 behaviour).
    assert store.resolve_current_tenant(s, "alice", "globex") is None
    # And globex cannot own acme's app through the tenant gate.
    assert store.resolve_app_owned(s, acme.id, "globex") is None


def test_leak_conversation_by_app_id_without_gate(seeded):
    s, acme, _, _ = seeded
    # Attacker holds acme's app_id, queries conversations WITHOUT the tenant gate.
    leaked = store.list_conversations_by_app_id_UNCHECKED(s, acme.id)
    assert any("SECRET-42" in c.name for c in leaked), "expected cross-tenant leak"


def test_leak_conversation_by_raw_id(seeded):
    s, _, _, conv = seeded
    got = store.get_conversation_by_id_UNCHECKED(s, conv.id)
    assert got is not None and "SECRET-42" in got.name  # no tenant_id to check


def test_leak_unscoped_segment_scan(seeded):
    s, _, _, _ = seeded
    segs = store.list_segments_UNSCOPED(s)
    assert any("SECRET-42" in d.content for d in segs), "forgot .where(tenant_id) = org-wide scan"
