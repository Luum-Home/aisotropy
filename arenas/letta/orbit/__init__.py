"""
Orbit — a multi-tenant support chatbot built on the REAL Letta (MemGPT) SDK.

This package depends on `letta-client` (the official Letta REST SDK) and uses its
real Pydantic memory/state types (BlockResponse, AgentState, Passage) and real
client resources (agents / blocks / passages / archives).

Two execution modes:

  * LIVE  — set LETTA_BASE_URL (and LETTA_API_KEY / LETTA_PROJECT_ID) to talk to a
            running Letta server. `orbit.client.build_client()` returns a real
            `letta_client.Letta`. Agent runs happen server-side.

  * OFFLINE — no server. `orbit.memory` provides a BlockStore/PassageStore that
            hold the REAL Letta types and replicate BOTH the server's org-wide,
            developer-passed list/search contract (identity_id / project_id /
            label / tags) AND the agent-scoped subresources
            (agents.blocks.list / agents.passages.search) that isolate by
            agent_id. Used by the isolation smokes to prove feature #1 against the
            real state model without an LLM key or a server. Clearly labelled as a
            behavioral simulation of the server's data plane.
"""

from .config import OrbitConfig, load_config
from .memory import BlockStore, PassageStore, TenantScope, new_agent_id

__all__ = [
    "OrbitConfig", "load_config",
    "BlockStore", "PassageStore", "TenantScope", "new_agent_id",
]
