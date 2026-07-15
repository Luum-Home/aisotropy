"""
OrbitService — the multi-tenant support chatbot, offline edition.

Ties the real Letta memory model (orbit.memory) to a deterministic responder
(orbit.agent.StubResponder). One process serves many tenants; each turn carries a
TenantScope. The service exposes both the correct (scoped) read path and the raw
primitives that leak, so the isolation smoke can exercise both.
"""

from __future__ import annotations

from .agent import StubResponder
from .memory import BlockStore, PassageStore, TenantScope


class OrbitService:
    def __init__(self) -> None:
        self.blocks = BlockStore()
        self.passages = PassageStore()
        self.responder = StubResponder(self.blocks, self.passages)

    def remember(self, scope: TenantScope, fact: str) -> None:
        """Write a per-tenant memory (core block + archival passage)."""
        self.blocks.create(
            label="human",
            value=fact,
            project_id=scope.project_id,
            tags=scope.tags,
        )
        self.passages.insert(text=fact, tags=scope.tags)

    def chat(self, scope: TenantScope, message: str) -> dict:
        """Correct path: answer using only the caller's scope."""
        return self.responder.answer(scope, message)

    def chat_unscoped(self, message: str) -> dict:
        """Leaky path: the developer forgot to pass a scope. One argument apart."""
        return self.responder.answer(None, message)
