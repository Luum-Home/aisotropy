"""
Orbit support agent — built on Letta's real agent/memory API.

LIVE path (needs a server): `create_agent` calls the real
`client.agents.create(...)` with real `memory_blocks`, `identity_ids`, `tags`,
`project_id`, and a structured `response_format`; `send_message` calls the real
`client.agents.messages.create(...)`.

OFFLINE path (default here): `build_agent_payload` returns the exact real
create-payload (so the shape is verifiable without a server), and
`StubResponder` answers deterministically over the offline memory model — no LLM
key, no network. This is the honest stub-model path required for CI.
"""

from __future__ import annotations

from dataclasses import dataclass

from .config import OrbitConfig
from .memory import BlockStore, PassageStore, TenantScope

# Orbit's persona + the two per-tenant core-memory blocks every support agent gets.
ORBIT_PERSONA = (
    "You are Orbit, a friendly multi-tenant SaaS support assistant. You only ever "
    "discuss the data belonging to the current tenant/user."
)


def build_agent_payload(cfg: OrbitConfig, scope: TenantScope) -> dict:
    """Build the REAL `client.agents.create(...)` payload for one principal.

    Uses the genuine Letta multi-tenancy levers: per-agent `memory_blocks`,
    `tags`, `project_id`, and (Cloud) `identity_ids`. Returns a plain dict so it
    can be asserted offline and splatted into the real API on the live path.

    Isolation note: Letta's documented multi-tenant pattern is ONE AGENT PER
    PRINCIPAL — reads then go through the agent-scoped subresources
    (`agents.blocks.list(agent_id)` / `agents.passages.search(agent_id)`), which
    isolate by `agent_id`. The agent name below keys on (tenant, user); when a
    finer axis such as product is in play, give each (tenant, user, product) its
    own agent so the boundary rides on `agent_id` rather than a `tags` convention
    (see `scripts/product_isolation_demo.py`).
    """
    return dict(
        name=f"orbit-{scope.tenant_id}-{scope.user_id}",
        model=cfg.model,
        # Structured output: Letta's real response_format lever (feature #2).
        response_format={"type": "json_schema", "json_schema": _reply_schema()},
        # Core memory blocks (feature #5, working memory).
        memory_blocks=[
            {"label": "persona", "value": ORBIT_PERSONA},
            {"label": "human", "value": f"Support user {scope.user_id} of {scope.tenant_id}."},
        ],
        # Multi-tenancy (feature #1) — ALL developer-passed, none enforced:
        tags=scope.tags,
        project_id=scope.project_id,
        # include_base_tools gives the agent core_memory_append / archival_memory_*
        # tools so it self-manages memory (feature #3, tool calling).
        include_base_tools=True,
    )


def _reply_schema() -> dict:
    return {
        "name": "orbit_reply",
        "schema": {
            "type": "object",
            "properties": {
                "answer": {"type": "string"},
                "escalate": {"type": "boolean"},
            },
            "required": ["answer", "escalate"],
            "additionalProperties": False,
        },
    }


def create_agent(client, cfg: OrbitConfig, scope: TenantScope):  # pragma: no cover
    """LIVE: create the real Letta agent on the server."""
    return client.agents.create(**build_agent_payload(cfg, scope))


def send_message(client, agent_id: str, text: str):  # pragma: no cover
    """LIVE: send one message to a real Letta agent."""
    return client.agents.messages.create(
        agent_id=agent_id,
        messages=[{"role": "user", "content": text}],
    )


@dataclass
class StubResponder:
    """Deterministic offline responder over the real memory model.

    Stands in for the LLM on the OFFLINE path. It reads ONLY through the scope it
    is given, so a correct scope yields correct answers and a wrong/absent scope
    yields the leak — exactly mirroring what the LLM would surface from whatever
    the developer's filters returned.
    """

    blocks: BlockStore
    passages: PassageStore

    def answer(self, scope: TenantScope | None, question: str) -> dict:
        # Core-memory read, scoped the way a careful developer would.
        if scope is not None:
            core = self.blocks.list(project_id=scope.project_id, tags=scope.tags)
            archival = self.passages.search(tags=scope.tags, query=None)
        else:
            # The leaky path: no scope → org-wide read.
            core = self.blocks.list()
            archival = self.passages.search()
        known = " ".join(b.value for b in core) + " " + " ".join(p.text or "" for p in archival)
        return {
            "answer": f"[Orbit] Based on your records: {known.strip()}",
            "escalate": False,
        }
