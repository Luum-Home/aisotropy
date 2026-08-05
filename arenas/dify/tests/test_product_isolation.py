"""
Multi-PRODUCT isolation asserted as executable tests (the 4th scope axis).
Finding: Dify has no product_id anywhere; a 'product' is app_id convention or an
unbound tag, and the RAG plane has no product boundary at all.

SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md
  Verified at langgenius/dify HEAD 4213e6b1: `grep -rn "product_id" api/` returns 7 hits,
  all of them ProviderOrder.payment_product_id (a billing SKU) or its tests/migration.
  Dataset/DocumentSegment carry tenant_id and no product/app/agent column.
  Note (same sweep): aisotropy does NOT enforce a product axis either — product_id is a
  persona selector, not part of memory.Key or any Scope. This is a shared gap.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import pytest  # noqa: E402

from orbit.models import App, Conversation, Dataset, DocumentSegment  # noqa: E402
from orbit import store  # noqa: E402


@pytest.fixture()
def two_products():
    s = store.new_session()
    checkout = App(tenant_id="acme", name="product:checkout")
    analytics = App(tenant_id="acme", name="product:analytics")
    s.add_all([checkout, analytics])
    s.flush()
    s.add(Conversation(app_id=checkout.id, from_end_user_id="alice", name="checkout PROD-SECRET"))
    kb = Dataset(tenant_id="acme", name="acme-shared-kb")
    s.add(kb)
    s.flush()
    s.add(DocumentSegment(tenant_id="acme", dataset_id=kb.id, content="checkout PROD-SECRET"))
    s.flush()
    return s, checkout, analytics


def test_no_product_id_column_exists():
    # Structural: not a single Orbit/Dify model carries a product_id column.
    for model in (App, Conversation, Dataset, DocumentSegment):
        assert "product_id" not in model.__table__.columns, f"{model.__name__} unexpectedly has product_id"


def test_rag_plane_has_no_product_boundary(two_products):
    s, _, _ = two_products
    # The ONLY scope on segments is tenant_id; product 'analytics' reading the
    # tenant-correct way still sees checkout's segment.
    segs = store.list_segments_scoped(s, "acme")
    assert any("PROD-SECRET" in d.content for d in segs), "no product axis to separate them"


def test_product_conversation_crosses_by_app_id(two_products):
    s, checkout, _ = two_products
    cross = store.list_conversations_by_app_id_UNCHECKED(s, checkout.id)
    assert any("PROD-SECRET" in c.name for c in cross)
