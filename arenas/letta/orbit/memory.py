"""
Offline Letta memory model — REAL types, real server surface (letta-client 1.12.1).

This module is a **clearly-labelled behavioral simulation of the Letta server's
data plane**. It does NOT invent a memory model: every stored object is a real
`letta_client.types.BlockResponse` / `Passage`, and the accessors replicate the
*documented* filter/scope contract of the installed `letta-client==1.12.1` SDK
exactly.

Letta exposes memory reads at **two access levels**, and the difference between
them is the whole isolation story:

  ORG-WIDE (top-level resources) — filters OPTIONAL, developer-passed:
    client.blocks.list(project_id=, identity_id=, label=, tags=, match_all_tags=)
    client.blocks.retrieve(block_id)                 # raw id, no tenant check
    client.passages.search(agent_id=, archive_id=, tags=, tag_match_mode=, query=)
        SDK docstring: "If neither agent_id nor archive_id is provided, searches
        ALL passages in the organization." Under-scope it and you scan the org.
        (`match_all_tags` / `tag_match_mode` default to ANY-tag, per the SDK.)

  AGENT-SCOPED (agents subresources) — `agent_id` REQUIRED, isolates by construction:
    client.agents.blocks.list(agent_id)              # only THIS agent's core memory
    client.agents.passages.search(agent_id, query=)  # only THIS agent's archives
        Route: /v1/agents/{agent_id}/archival-memory/search. The SDK rejects an
        empty agent_id. A read cannot span agents through this surface.

The isolation truth this exposes is Letta's real one: **OSS Letta is a SINGLE
ORG with no framework binding between an authenticated caller and a tenant.** The
org-wide primitives return another tenant's data whenever you under-scope them
(`blocks.list()` with no filter is an org-wide scan; `blocks.retrieve(id)` does no
tenant check). The agent-scoped subresources DO isolate — but by `agent_id`, and
it is on the *app* to map (verified caller -> agent_id). Letta's documented
multi-tenant pattern is therefore **one agent per principal** (see README /
https://docs.letta.com/guides/agents/multi-user). Per-*user* API-key isolation
exists only in Letta Cloud (per-project keys), not OSS.
"""

from __future__ import annotations

import secrets
from dataclasses import dataclass

from letta_client.types import BlockResponse, Passage


def _new_id(prefix: str) -> str:
    return f"{prefix}-{secrets.token_hex(8)}"


def new_agent_id() -> str:
    """A real-shaped Letta agent id (`agent-<uuid4-ish>`). The agent id is the
    isolation key for the agent-scoped subresources."""
    return f"agent-{secrets.token_hex(16)}"


@dataclass(frozen=True)
class TenantScope:
    """The scope a developer *should* attach to every ORG-WIDE read/write.

    In Letta's top-level resources this is not enforced — it is exactly the set of
    optional query params the developer chooses to pass. Making it a value object
    here is what lets the smoke show the correct path and the leaky path are one
    argument apart (pass `scope` vs. omit it).
    """

    tenant_id: str
    user_id: str

    @property
    def project_id(self) -> str:
        # Letta Cloud maps a tenant onto a project_id; OSS has a single org.
        return f"proj-{self.tenant_id}"

    @property
    def tags(self) -> list[str]:
        # The developer-passed tag convention that scopes blocks/passages.
        return [f"tenant:{self.tenant_id}", f"user:{self.user_id}"]


class BlockStore:
    """Replicates `client.blocks` (org-wide) + `client.agents.blocks` (scoped).

    Stores real `BlockResponse` objects. The top-level `list()` applies the same
    optional, developer-passed filters as the real server API; the agent-scoped
    `agent_blocks_list()` isolates by `agent_id` exactly like the real
    `client.agents.blocks.list(agent_id)`.
    """

    def __init__(self) -> None:
        self._blocks: dict[str, BlockResponse] = {}
        # agent_id -> set(block_id): the real attach relation (memory_blocks /
        # agents.blocks.attach). A block is visible on the agent-scoped surface
        # only for the agents it is attached to.
        self._agent_blocks: dict[str, set[str]] = {}

    # ── writes ──────────────────────────────────────────────────────────────
    def create(
        self,
        *,
        label: str,
        value: str,
        project_id: str | None = None,
        identity_ids: list[str] | None = None,
        tags: list[str] | None = None,
        agent_id: str | None = None,
        limit: int = 5000,
    ) -> BlockResponse:
        """Mirror of `client.blocks.create(label=, value=, ...)`.

        `agent_id` records the attach relation (as `agents.create(memory_blocks=)`
        / `agents.blocks.attach` would); it is store bookkeeping, not a field on
        the real `BlockResponse`.
        """
        block = BlockResponse(
            id=_new_id("block"),
            label=label,
            value=value,
            limit=limit,
            project_id=project_id,
            tags=tags or [],
            metadata={"identity_ids": identity_ids or []},
        )
        self._blocks[block.id] = block
        if agent_id is not None:
            self._agent_blocks.setdefault(agent_id, set()).add(block.id)
        return block

    def update(
        self,
        block_id: str,
        *,
        value: str | None = None,
        label: str | None = None,
        limit: int | None = None,
    ) -> BlockResponse | None:
        """Mirror of `client.blocks.update(block_id, value=, label=, limit=)`
        (agent-scoped twin: `client.agents.blocks.update(block_label, agent_id,
        value=)`).

        This is Letta's **self-editing memory** primitive — the MemGPT heritage.
        An agent rewrites its OWN core-memory block in place: the base tools
        `core_memory_append` / `core_memory_replace` (granted by
        `include_base_tools=True`) drive exactly this update server-side. Here we
        call it directly and deterministically. The block id (and any agent
        attachment) is preserved, so the edit persists as durable state.
        """
        block = self._blocks.get(block_id)
        if block is None:
            return None
        patch = {k: v for k, v in (("value", value), ("label", label),
                                   ("limit", limit)) if v is not None}
        updated = block.model_copy(update=patch)
        self._blocks[block_id] = updated
        return updated

    # ── reads: ORG-WIDE (top-level client.blocks) ────────────────────────────
    def retrieve(self, block_id: str) -> BlockResponse | None:
        """Mirror of `client.blocks.retrieve(block_id)`.

        NOTE: the real server performs NO tenant check here. Any caller holding a
        block id reads that block. This is a genuine single-org read primitive —
        analogous to a row PK with no row-level security: it only bites a caller
        who already holds a foreign id, but the framework will not stop them.
        """
        return self._blocks.get(block_id)

    def list(
        self,
        *,
        project_id: str | None = None,
        identity_id: str | None = None,
        label: str | None = None,
        tags: list[str] | None = None,
        match_all_tags: bool = False,
    ) -> list[BlockResponse]:
        """Mirror of `client.blocks.list(...)` (the ORG-WIDE core-memory table).

        Every filter is OPTIONAL and developer-passed, exactly like the real API.
        Omitting all filters returns EVERY block in the org — the org-wide scan
        that leaks across tenants. Per the SDK, the default (`match_all_tags`
        unset / False) matches blocks with ANY of the passed tags, not all.
        """
        results: list[BlockResponse] = []
        for block in self._blocks.values():
            if project_id is not None and block.project_id != project_id:
                continue
            if label is not None and block.label != label:
                continue
            if identity_id is not None:
                ids = (block.metadata or {}).get("identity_ids", [])
                if identity_id not in ids:
                    continue
            if tags:
                btags = set(block.tags or [])
                if match_all_tags:
                    if not set(tags).issubset(btags):
                        continue
                else:
                    if not (set(tags) & btags):
                        continue
            results.append(block)
        return results

    # ── reads: AGENT-SCOPED (client.agents.blocks) ───────────────────────────
    def agent_blocks_list(self, agent_id: str) -> list[BlockResponse]:
        """Mirror of `client.agents.blocks.list(agent_id)`.

        `agent_id` is REQUIRED (the SDK rejects an empty value) and the result is
        exactly the core-memory blocks attached to THAT agent — never another
        agent's. Isolation is by construction: there is no argument to omit that
        would widen the scan to the org.
        """
        if not agent_id:
            raise ValueError("agent_id required (format 'agent-<uuid4>')")
        ids = self._agent_blocks.get(agent_id, set())
        return [self._blocks[i] for i in ids if i in self._blocks]


class PassageStore:
    """Replicates `client.passages` (org-wide) + `client.agents.passages` (scoped).

    Stores real `Passage` objects. `search()` mirrors the real top-level
    archival-search contract (org-wide unless narrowed by `agent_id`/`archive_id`);
    `agent_search()` mirrors the agent-scoped `client.agents.passages.search`,
    which isolates by `agent_id`.
    """

    def __init__(self) -> None:
        self._passages: list[Passage] = []
        # agent_id -> [passage_id]: passages reachable from an agent's archives.
        self._agent_passages: dict[str, list[str]] = {}

    def insert(
        self,
        *,
        text: str,
        tags: list[str] | None = None,
        archive_id: str | None = None,
        agent_id: str | None = None,
    ) -> Passage:
        """Mirror of inserting an archival passage (into an agent's archive)."""
        passage = Passage(
            id=_new_id("passage"),
            text=text,
            tags=tags or [],
            archive_id=archive_id,
        )
        self._passages.append(passage)
        if agent_id is not None:
            self._agent_passages.setdefault(agent_id, []).append(passage.id)
        return passage

    def _match(
        self,
        p: Passage,
        *,
        query: str | None,
        tags: list[str] | None,
        tag_match_mode: str,
        archive_id: str | None,
    ) -> bool:
        if archive_id is not None and p.archive_id != archive_id:
            return False
        if tags:
            ptags = set(p.tags or [])
            if tag_match_mode == "all":
                if not set(tags).issubset(ptags):
                    return False
            else:
                if not (set(tags) & ptags):
                    return False
        # Substring match stands in for the server's vector similarity (offline,
        # no embeddings). The *scope-relevant* semantics — tag/archive/agent — are
        # faithful.
        if query is not None and query.lower() not in (p.text or "").lower():
            return False
        return True

    def search(
        self,
        *,
        agent_id: str | None = None,
        query: str | None = None,
        tags: list[str] | None = None,
        tag_match_mode: str = "any",
        archive_id: str | None = None,
    ) -> list[Passage]:
        """Mirror of the ORG-WIDE `client.passages.search(...)`.

        Faithful to the real SDK's documented precedence:
          * neither `agent_id` nor `archive_id` -> searches ALL org passages
            (the org-wide footgun);
          * `agent_id` given -> restricted to that agent's archives (precedence
            over `archive_id`);
          * else `archive_id` given -> restricted to that archive.
        `tag_match_mode` defaults to "any" (ANY-tag), like the real API.
        """
        if agent_id is not None:
            candidate_ids = set(self._agent_passages.get(agent_id, []))
            pool = [p for p in self._passages if p.id in candidate_ids]
        else:
            pool = self._passages
        return [
            p for p in pool
            if self._match(p, query=query, tags=tags,
                           tag_match_mode=tag_match_mode, archive_id=archive_id)
        ]

    def agent_search(
        self,
        agent_id: str,
        *,
        query: str | None = None,
        tags: list[str] | None = None,
        tag_match_mode: str = "any",
    ) -> list[Passage]:
        """Mirror of `client.agents.passages.search(agent_id, query=...)`.

        `agent_id` is REQUIRED (the SDK rejects an empty value) and only passages
        in THAT agent's archives are ever in scope. Isolation is by construction.
        """
        if not agent_id:
            raise ValueError("agent_id required (format 'agent-<uuid4>')")
        candidate_ids = set(self._agent_passages.get(agent_id, []))
        return [
            p for p in self._passages
            if p.id in candidate_ids
            and self._match(p, query=query, tags=tags,
                            tag_match_mode=tag_match_mode, archive_id=None)
        ]
