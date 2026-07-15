"""
Pytest form of the Letta isolation matrix — asserts against the REAL SDK types.

These tests encode the arena's thesis as executable assertions:
  * the correctly-scoped read is clean (no cross-tenant marker), and
  * every Letta leaky primitive (unscoped list / raw retrieve / unscoped search)
    IS a cross-tenant read — that is the finding, asserted as True.
"""

from __future__ import annotations

import os
import secrets
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from letta_client.types import BlockResponse, Passage  # noqa: E402
from orbit.memory import BlockStore, PassageStore, TenantScope  # noqa: E402


@pytest.fixture
def seeded():
    blocks = BlockStore()
    passages = PassageStore()
    a = TenantScope(tenant_id="acme", user_id="alice")
    b = TenantScope(tenant_id="globex", user_id="bob")
    tok = f"lt-{secrets.token_hex(6)}"
    block = blocks.create(label="human", value=f"secret {tok}",
                          project_id=a.project_id, tags=a.tags)
    passages.insert(text=f"secret {tok}", tags=a.tags)
    return blocks, passages, a, b, tok, block


def test_stored_objects_are_real_letta_types(seeded):
    blocks, passages, *_ , block = seeded
    assert isinstance(block, BlockResponse)
    assert isinstance(passages.search()[0], Passage)


def test_positive_control(seeded):
    blocks, _, a, _, tok, _ = seeded
    own = blocks.list(project_id=a.project_id, tags=a.tags)
    assert any(tok in x.value for x in own)


def test_correct_cross_tenant_scope_is_clean(seeded):
    blocks, _, _, b, tok, _ = seeded
    other = blocks.list(project_id=b.project_id, tags=b.tags)
    assert not any(tok in x.value for x in other)


def test_leak_unscoped_list(seeded):
    blocks, _, _, _, tok, _ = seeded
    org_wide = blocks.list()  # no filter == org-wide scan
    assert any(tok in x.value for x in org_wide), "unscoped list should leak"


def test_leak_retrieve_by_id_has_no_tenant_check(seeded):
    blocks, _, _, _, tok, block = seeded
    grabbed = blocks.retrieve(block.id)
    assert grabbed is not None and tok in grabbed.value


def test_leak_unscoped_archival_search(seeded):
    _, passages, _, _, tok, _ = seeded
    assert any(tok in (p.text or "") for p in passages.search())


def test_wrong_identity_filter_returns_wrong_tenant(seeded):
    blocks, _, a, _, tok, _ = seeded
    # Attach an identity to acme's world, then query with acme's identity from a
    # caller who should be globex: developer-passed identity == cross read.
    blocks.create(label="human", value=f"more {tok}",
                  project_id=a.project_id, tags=a.tags,
                  identity_ids=["identity-acme"])
    hit = blocks.list(identity_id="identity-acme")
    assert any(tok in x.value for x in hit)
