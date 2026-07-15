"""
Offline Letta memory model — REAL types, real server filter contract.

This module is a **clearly-labelled behavioral simulation of the Letta server's
data plane**. It does NOT invent a memory model: every stored object is a real
`letta_client.types.BlockResponse` / `Passage`, and `list()` / `search()`
replicate the server's *documented, developer-passed* filter contract exactly
(the same optional `identity_id` / `project_id` / `label` / `tags` parameters the
real `client.blocks.list()` / `client.passages.search()` accept).

Why a simulation at all: Letta agents and their memory live on a SERVER
(`letta server` / Docker / Letta Cloud). This offline environment has none, and
we do not fake a connection. To prove feature #1 (tenant isolation) against the
*real* Letta state model without a server, we reconstruct only the block/passage
tables and their filter semantics, using the genuine SDK types.

The isolation truth this exposes is Letta's real one: **multi-tenancy is
developer-passed.** There is no framework binding between an authenticated caller
and a tenant. `blocks.list()` with no filter returns every tenant's block;
`blocks.retrieve(id)` performs no tenant check. Per-*user* key isolation exists
only in Letta Cloud (per-project API keys), not in OSS.
"""

from __future__ import annotations

import secrets
from dataclasses import dataclass, field

from letta_client.types import BlockResponse, Passage


def _new_id(prefix: str) -> str:
    return f"{prefix}-{secrets.token_hex(8)}"


@dataclass(frozen=True)
class TenantScope:
    """The scope a developer *should* attach to every read/write.

    In Letta this is not enforced — it is exactly the set of optional query
    params the developer chooses to pass. Making it a value object here is what
    lets the smoke show the correct path and the leaky path are one argument
    apart (pass `scope` vs. omit it).
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
    """Replicates `client.blocks` — the Letta core-memory block table.

    Stores real `BlockResponse` objects. `list()` applies the same optional,
    AND-combined, developer-passed filters as the real server API.
    """

    def __init__(self) -> None:
        self._blocks: dict[str, BlockResponse] = {}

    # ── writes ──────────────────────────────────────────────────────────────
    def create(
        self,
        *,
        label: str,
        value: str,
        project_id: str | None = None,
        identity_ids: list[str] | None = None,
        tags: list[str] | None = None,
        limit: int = 5000,
    ) -> BlockResponse:
        """Mirror of `client.blocks.create(label=, value=, ...)`."""
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
        return block

    # ── reads ───────────────────────────────────────────────────────────────
    def retrieve(self, block_id: str) -> BlockResponse | None:
        """Mirror of `client.blocks.retrieve(block_id)`.

        NOTE: the real server performs NO tenant check here. Any caller holding a
        block id reads that block. This is a genuine cross-tenant read primitive.
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
        """Mirror of `client.blocks.list(...)`.

        Every filter is OPTIONAL and developer-passed, exactly like the real API.
        Omitting all filters returns EVERY block in the org — the org-wide scan
        that leaks across tenants.
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


class PassageStore:
    """Replicates `client.passages` — Letta archival (vector) memory.

    Stores real `Passage` objects. `search()` mirrors the real, tag-scoped,
    developer-passed archival search contract.
    """

    def __init__(self) -> None:
        self._passages: list[Passage] = []

    def insert(
        self,
        *,
        text: str,
        tags: list[str] | None = None,
        archive_id: str | None = None,
    ) -> Passage:
        """Mirror of inserting an archival passage."""
        passage = Passage(
            id=_new_id("passage"),
            text=text,
            tags=tags or [],
            archive_id=archive_id,
        )
        self._passages.append(passage)
        return passage

    def search(
        self,
        *,
        query: str | None = None,
        tags: list[str] | None = None,
        tag_match_mode: str = "any",
        archive_id: str | None = None,
    ) -> list[Passage]:
        """Mirror of `client.passages.search(query=, tags=, tag_match_mode=, ...)`.

        Substring match stands in for the server's vector similarity (offline, no
        embeddings). The *tenant-relevant* semantics — tag/archive scoping — are
        faithful. Omit `tags` and every tenant's passages are in scope.
        """
        out: list[Passage] = []
        for p in self._passages:
            if archive_id is not None and p.archive_id != archive_id:
                continue
            if tags:
                ptags = set(p.tags or [])
                if tag_match_mode == "all":
                    if not set(tags).issubset(ptags):
                        continue
                else:
                    if not (set(tags) & ptags):
                        continue
            if query is not None and query.lower() not in (p.text or "").lower():
                continue
            out.append(p)
        return out
