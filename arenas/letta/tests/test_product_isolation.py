"""
Pytest form of the Letta MULTI-PRODUCT scoping matrix (the 3rd scope axis).

aisotropy scopes memory by (tenant_id, user_id, session_id, product_id). The
tenant/user axes are covered by test_isolation.py; this file encodes the product
axis HONESTLY, against the real letta-client 1.12.1 surface:

  * STRONGEST CONFIG (what a competent dev does): one agent per (tenant,user,
    product); reads via the AGENT-SCOPED subresources
    (agents.blocks.list(agent_id) / agents.passages.search(agent_id)) isolate by
    agent_id — a sibling product's agent sees NOTHING. Asserted clean.
  * Residual, narrower findings that remain ONLY on the ORG-WIDE primitives:
    the single-org top-level list has no caller binding; the SDK default
    match_all_tags=False (ANY-tag) is an unsafe DEFAULT one parameter from
    correct; blocks.retrieve(id) has no ownership check but needs a foreign id.
"""

from __future__ import annotations

import os
import secrets
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from letta_client.types import BlockResponse  # noqa: E402
from orbit.memory import BlockStore, PassageStore, new_agent_id  # noqa: E402


def _tenant_user_tags(tenant: str, user: str) -> list[str]:
    return [f"tenant:{tenant}", f"user:{user}"]


def _full_tags(tenant: str, user: str, product: str) -> list[str]:
    return [*_tenant_user_tags(tenant, user), f"product:{product}"]


@pytest.fixture
def seeded():
    blocks = BlockStore()
    passages = PassageStore()
    tok = f"lt-{secrets.token_hex(6)}"
    checkout_agent = new_agent_id()
    analytics_agent = new_agent_id()
    # Seed product 'checkout' on its OWN agent (agent-scoped) and on the org-wide
    # table with the full 3-axis tags (so org-wide footgun tests have a target).
    block = blocks.create(
        label="human", value=f"secret {tok}",
        project_id="proj-acme", tags=_full_tags("acme", "alice", "checkout"),
        agent_id=checkout_agent,
    )
    passages.insert(text=f"secret {tok}", tags=_full_tags("acme", "alice", "checkout"),
                    agent_id=checkout_agent)
    return blocks, passages, tok, block, checkout_agent, analytics_agent


def test_seeded_is_real_letta_type(seeded):
    _, _, _, block, _, _ = seeded
    assert isinstance(block, BlockResponse)


def test_positive_control_agent_scoped(seeded):
    blocks, _, tok, _, checkout_agent, _ = seeded
    own = blocks.agent_blocks_list(checkout_agent)
    assert any(tok in b.value for b in own)


def test_strongest_config_agent_scoped_blocks_isolate(seeded):
    # One agent per principal: analytics' agent reads its own core memory via the
    # agent-scoped subresource. It must NOT see checkout's marker — isolated by
    # agent_id, by construction, with no product tag involved.
    blocks, _, tok, _, _, analytics_agent = seeded
    scoped = blocks.agent_blocks_list(analytics_agent)
    assert not any(tok in b.value for b in scoped), \
        "agent-scoped block read must isolate by agent_id"


def test_strongest_config_agent_scoped_archival_isolates(seeded):
    _, passages, tok, _, _, analytics_agent = seeded
    scoped = passages.agent_search(analytics_agent, query=None)
    assert not any(tok in (p.text or "") for p in scoped), \
        "agents.passages.search(agent_id) must isolate by agent_id"


def test_agent_scoped_search_requires_agent_id(seeded):
    # The real client.agents.passages.search / agents.blocks.list reject an empty
    # agent_id (there is no argument to omit that widens the scan to the org).
    blocks, passages, *_ = seeded
    with pytest.raises(ValueError):
        blocks.agent_blocks_list("")
    with pytest.raises(ValueError):
        passages.agent_search("", query=None)


def test_org_wide_list_is_single_org_no_caller_binding(seeded):
    # Residual, NOT a distinct product mechanism: the org-wide top-level list has
    # no product filter and no caller binding. Scoping by tenant/user tags (the
    # only org-wide axes) returns checkout's block to an analytics code path. This
    # is the SAME single-org finding test_isolation.py proves for tenant/user.
    blocks, _, tok, _, _, _ = seeded
    hit = blocks.list(project_id="proj-acme",
                      tags=_tenant_user_tags("acme", "alice"),
                      match_all_tags=True)
    assert any(tok in b.value for b in hit)


def test_unsafe_default_match_all_tags_false_is_footgun(seeded):
    # UNSAFE DEFAULT (not a structural leak): on the org-wide surface the SDK
    # default match_all_tags=False (ANY-tag / OR) matches on the shared
    # tenant/user tags even when analytics passes its OWN product tag. The fix is
    # one parameter away — match_all_tags=True excludes checkout's block.
    blocks, _, tok, _, _, _ = seeded
    leaky = blocks.list(project_id="proj-acme",
                        tags=_full_tags("acme", "alice", "analytics"),
                        match_all_tags=False)
    fixed = blocks.list(project_id="proj-acme",
                        tags=_full_tags("acme", "alice", "analytics"),
                        match_all_tags=True)
    assert any(tok in b.value for b in leaky), "ANY-tag default matches the sibling"
    assert not any(tok in b.value for b in fixed), "match_all_tags=True is the fix"


def test_retrieve_by_id_has_no_ownership_check_but_needs_foreign_id(seeded):
    # Genuine single-org primitive: blocks.retrieve(id) applies no ownership
    # check. It only bites a caller who already holds the foreign id (row PK, no
    # row-level security) — not an ambient org-wide scan.
    blocks, _, tok, block, _, _ = seeded
    grabbed = blocks.retrieve(block.id)
    assert grabbed is not None and tok in grabbed.value
