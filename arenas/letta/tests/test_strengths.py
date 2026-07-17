"""
Pytest form of the Letta STRENGTHS steelman (the other half of the comparison).

The isolation tests are adversarial; these assert what Letta is genuinely great
at, grounded in the real letta-client 1.12.1 surface:

  * SELF-EDITING core memory (MemGPT heritage): an agent rewrites its OWN
    core-memory block in place via client.blocks.update / core_memory_replace,
    and the edit PERSISTS as durable state (run-verified, offline).
  * The server-backed strengths (sleep-time consolidation, cross-restart
    durability) are verified only at the SDK-symbol level offline — the real
    levers exist on the pinned types — and executed only against a live server.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from letta_client.types import AgentState, BlockResponse  # noqa: E402
from letta_client.types.agent_create_params import AgentCreateParams  # noqa: E402
from orbit.memory import BlockStore, new_agent_id  # noqa: E402


def test_self_edit_rewrites_core_memory_in_place():
    blocks = BlockStore()
    agent_id = new_agent_id()
    human = blocks.create(label="human", value="Name: unknown.", agent_id=agent_id)
    assert isinstance(human, BlockResponse)

    updated = blocks.update(human.id, value="Name: Ada. Plan: Pro.")
    assert isinstance(updated, BlockResponse)
    assert updated.id == human.id                      # same durable object
    assert updated.value == "Name: Ada. Plan: Pro."


def test_self_edit_persists_and_is_agent_scoped():
    blocks = BlockStore()
    agent_id = new_agent_id()
    human = blocks.create(label="human", value="v0", agent_id=agent_id)
    blocks.update(human.id, value="v1")
    # Re-read through the agent-scoped subresource: the edit persisted.
    got = blocks.agent_blocks_list(agent_id)
    assert len(got) == 1
    assert got[0].value == "v1"


def test_self_edit_append_accumulates_across_turns():
    blocks = BlockStore()
    agent_id = new_agent_id()
    human = blocks.create(label="human", value="a", agent_id=agent_id)
    blocks.update(human.id, value="a b")
    blocks.update(human.id, value="a b c")
    assert blocks.agent_blocks_list(agent_id)[0].value == "a b c"


def test_update_missing_block_returns_none():
    blocks = BlockStore()
    assert blocks.update("block-does-not-exist", value="x") is None


def test_sleeptime_lever_exists_on_pinned_sdk():
    # agents.create(enable_sleeptime=True) — the sleep-time consolidation lever.
    assert "enable_sleeptime" in AgentCreateParams.__annotations__


def test_agentstate_carries_durable_state_shape():
    # The MemGPT agent-as-a-service heritage: durable server-side state object.
    fields = set(AgentState.model_fields.keys())
    for f in ("id", "memory", "blocks", "tools", "sources", "enable_sleeptime"):
        assert f in fields
