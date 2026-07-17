#!/usr/bin/env python3
"""
Letta (MemGPT) STRENGTHS demo — steelman, grounded in real letta-client 1.12.1.

The isolation smokes in this arena are adversarial by design. This demo is the
other half of an honest comparison: what Letta is **genuinely great at**, shown
against the REAL pinned SDK surface. Where the offline/no-key path can execute it,
we execute it (self-editing core memory, run-verified below). Where a capability
is inherently server-backed (sleep-time compute, cross-restart durability), we
describe it precisely from the pinned SDK symbols and label it NOT-RUN — no fake
server, no fabricated output.

Strengths shown:
  1. SELF-EDITING core memory (MemGPT heritage)      — RUN-VERIFIED (offline)
  2. Sleep-time memory consolidation                 — DESCRIBED (server-backed)
  3. Stateful agent-as-a-service (durable AgentState)— DESCRIBED (server-backed),
                                                        state SHAPE shown offline
"""

from __future__ import annotations

import os
import sys

try:
    import letta_client  # noqa: F401
    from letta_client.types import AgentState, BlockResponse  # noqa: F401
except ImportError:
    print("ERROR: letta-client not installed. Run: pip install -r requirements.txt",
          file=sys.stderr)
    sys.exit(1)

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.memory import BlockStore, new_agent_id  # noqa: E402


def strength_self_editing_memory() -> tuple[bool, list[str]]:
    """1. SELF-EDITING core memory — RUN-VERIFIED against real BlockResponse.

    Real SDK surface: `client.blocks.update(block_id, value=...)` and the
    agent-scoped `client.agents.blocks.update(block_label, agent_id, value=...)`.
    With `include_base_tools=True`, the LLM edits its OWN core memory via the
    `core_memory_append` / `core_memory_replace` tools, which drive that same
    update. We call it directly and deterministically and prove the edit persists.
    """
    ok = True
    notes: list[str] = []
    blocks = BlockStore()
    agent_id = new_agent_id()

    # The agent's initial `human` core-memory block (a real BlockResponse).
    human = blocks.create(
        label="human",
        value="Name: unknown. Plan: unknown.",
        agent_id=agent_id,
    )
    assert isinstance(human, BlockResponse)
    print(f"[letta] Self-editing memory — agent {agent_id[:14]}… core block "
          f"{human.id[:14]}…")
    print(f"  before: {human.value!r}")

    # Turn 1: the agent learns the user's name and rewrites its own core memory
    # (this is what core_memory_replace does server-side).
    blocks.update(human.id, value="Name: Ada. Plan: Pro (annual). Timezone: CET.")

    # The edit persists as durable state: re-read via the agent-scoped subresource.
    after = blocks.agent_blocks_list(agent_id)[0]
    print(f"  after : {after.value!r}")
    if after.value != "Name: Ada. Plan: Pro (annual). Timezone: CET.":
        notes.append("  FAIL self-edit did not persist")
        return False, notes
    notes.append(
        "  OK  agent rewrote its own core-memory block in place; edit persisted "
        "(real BlockResponse, client.blocks.update / core_memory_replace)")

    # Turn 2: append a fact — the agent keeps curating its own context window.
    blocks.update(after.id,
                  value=after.value + " Note: prefers email over phone.")
    final = blocks.agent_blocks_list(agent_id)[0]
    if "prefers email" not in final.value:
        notes.append("  FAIL append did not persist")
        return False, notes
    notes.append(
        "  OK  second self-edit (append) persisted — the agent curates its own "
        "working memory across turns")
    print(f"  final : {final.value!r}")
    return ok, notes


def strength_sleeptime_described() -> list[str]:
    """2. Sleep-time consolidation — DESCRIBED (server-backed, NOT-RUN).

    Real SDK surface (pinned): `agents.create(enable_sleeptime=True)` — the
    param's own docstring: "If set to True, memory management will move to a
    background agent thread." `AgentState.enable_sleeptime` reflects it. A paired
    background agent consolidates memory during idle time. A related in-band lever
    is `client.agents.messages.compact(...)` — "Summarize an agent's conversation
    history." Both require a running server (+ provider key) to actually execute,
    so they are described, not faked.
    """
    notes: list[str] = []
    # We CAN verify the lever exists on the pinned type, offline:
    from letta_client.types.agent_create_params import AgentCreateParams  # noqa: E402
    has_sleeptime = "enable_sleeptime" in AgentCreateParams.__annotations__
    notes.append(
        f"  SDK-VERIFIED enable_sleeptime present on AgentCreateParams: {has_sleeptime}")
    notes.append(
        "  NOT-RUN (server-backed): a paired background agent consolidates memory "
        "on a separate thread; also agents.messages.compact() summarizes history")
    print("[letta] Sleep-time consolidation — agents.create(enable_sleeptime=True)")
    print(f"  SDK param present offline: {has_sleeptime}; execution needs a server (NOT-RUN)")
    return notes


def strength_stateful_agent_described() -> list[str]:
    """3. Stateful agent-as-a-service — DESCRIBED (server-backed), SHAPE shown.

    Real SDK surface (pinned): a Letta agent is a durable server-side object,
    `AgentState`, carrying `id`, `memory` / `blocks`, `tools`, `sources`,
    `enable_sleeptime`, `identity_ids`. Its message history persists and is
    queryable via `client.agents.messages.list(agent_id)`; `agents.messages.reset`
    exists too. The agent survives process/server restarts because the state lives
    in the server's DB — the MemGPT "agent-as-a-service" heritage. Cross-restart
    durability needs the real server+DB (NOT-RUN), but we can show the real state
    SHAPE offline.
    """
    notes: list[str] = []
    fields = set(AgentState.model_fields.keys())
    durable = [f for f in ("id", "memory", "blocks", "tools", "sources",
                           "enable_sleeptime", "identity_ids") if f in fields]
    notes.append(
        f"  SDK-VERIFIED AgentState carries durable state fields: {durable}")
    notes.append(
        "  NOT-RUN (server-backed): cross-restart persistence + "
        "agents.messages.list(agent_id) recall history live in the server DB")
    print("[letta] Stateful agent-as-a-service — durable AgentState (MemGPT heritage)")
    print(f"  AgentState durable fields present offline: {durable}")
    return notes


def main() -> int:
    print("=" * 64)
    print("Letta (MemGPT) — Strengths Demo (steelman, real SDK 1.12.1)")
    print("=" * 64)
    print()

    details: list[str] = []

    passed, notes = strength_self_editing_memory()
    details += notes
    print()
    details += strength_sleeptime_described()
    print()
    details += strength_stateful_agent_described()

    print()
    print("-" * 64)
    for d in details:
        print(d)
    print("-" * 64)
    print()
    print("[letta] These are genuinely differentiated memory capabilities. aisotropy")
    print("  has tiered memory too, but does NOT productize self-editing core-memory")
    print("  blocks or a sleep-time consolidation agent the way Letta does — Letta is")
    print("  the memory-OS product; aisotropy is the isolation-by-construction runtime.")

    # Exit 0 when the run-verified strength (self-editing memory) holds.
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
