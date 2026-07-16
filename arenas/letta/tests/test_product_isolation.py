"""
Pytest form of the Letta MULTI-PRODUCT isolation matrix (the 3rd scope axis).

aisotropy scopes memory by (tenant_id, user_id, session_id, product_id). The
tenant/user axes are covered by test_isolation.py; this file encodes the product
axis. Thesis, as executable assertions:
  * a full three-axis scope reads its own product's marker (positive control), and
  * product_id binds to NOTHING in Letta — its real list/search API has no
    product filter — so a sibling product (same tenant+user) reads across the
    product boundary. That cross-product read is the finding, asserted True.
"""

from __future__ import annotations

import os
import secrets
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from letta_client.types import BlockResponse  # noqa: E402
from orbit.memory import BlockStore, PassageStore  # noqa: E402


def _tenant_user_tags(tenant: str, user: str) -> list[str]:
    return [f"tenant:{tenant}", f"user:{user}"]


def _full_tags(tenant: str, user: str, product: str) -> list[str]:
    return [*_tenant_user_tags(tenant, user), f"product:{product}"]


@pytest.fixture
def seeded():
    blocks = BlockStore()
    passages = PassageStore()
    tok = f"lt-{secrets.token_hex(6)}"
    # Seed product 'checkout' for tenant=acme, user=alice with the full 3-axis tags.
    block = blocks.create(
        label="human", value=f"secret {tok}",
        project_id="proj-acme", tags=_full_tags("acme", "alice", "checkout"),
    )
    passages.insert(text=f"secret {tok}", tags=_full_tags("acme", "alice", "checkout"))
    return blocks, passages, tok, block


def test_seeded_is_real_letta_type(seeded):
    _, _, _, block = seeded
    assert isinstance(block, BlockResponse)


def test_positive_control_same_product(seeded):
    blocks, _, tok, _ = seeded
    own = blocks.list(project_id="proj-acme",
                      tags=_full_tags("acme", "alice", "checkout"),
                      match_all_tags=True)
    assert any(tok in b.value for b in own)


def test_leak_sibling_product_via_tenant_user_scope(seeded):
    # Same tenant+user, product 'analytics' scopes by tenant/user only (Letta has
    # no product filter to pass) -> reads checkout's marker.
    blocks, _, tok, _ = seeded
    hit = blocks.list(project_id="proj-acme",
                      tags=_tenant_user_tags("acme", "alice"),
                      match_all_tags=True)
    assert any(tok in b.value for b in hit), "sibling product should read across"


def test_leak_default_any_tag_match_crosses_product(seeded):
    # Real Letta default is match_all_tags=False (ANY tag). analytics passes its
    # own full tags but the shared tenant/user tags alone match checkout's block.
    blocks, _, tok, _ = seeded
    hit = blocks.list(project_id="proj-acme",
                      tags=_full_tags("acme", "alice", "analytics"),
                      match_all_tags=False)
    assert any(tok in b.value for b in hit)


def test_leak_retrieve_by_id_has_no_product_check(seeded):
    blocks, _, tok, block = seeded
    grabbed = blocks.retrieve(block.id)
    assert grabbed is not None and tok in grabbed.value


def test_leak_archival_search_crosses_product(seeded):
    _, passages, tok, _ = seeded
    hits = passages.search(tags=_tenant_user_tags("acme", "alice"), tag_match_mode="all")
    assert any(tok in (p.text or "") for p in hits)
