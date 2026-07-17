"""
Orbit store — replicates Dify's REAL application-layer tenant-filter contract.

Every query below is written the way Dify's own services write it, so the
"correct scoped read" and the "leaky read" are exactly as far apart as they are
in the real codebase — which is the whole point of the isolation smoke.

Real-source contract this mirrors:
  - conversation_service.py:51  select(Conversation).where(Conversation.app_id == app_model.id)
      -> Conversations are fetched by app_id. app_model must be tenant-resolved FIRST.
  - AppService / app list endpoints  select(App).where(App.tenant_id == current_tenant_id)
      -> Apps are fetched by tenant_id (App has the column).
  - DatasetService                   select(Dataset).where(Dataset.tenant_id == current_tenant_id)

The tenant boundary is a Python `.where(...)` the developer must remember on
every read. Nothing in Postgres enforces it (no RLS, no schema-per-tenant).
"""

from __future__ import annotations

from sqlalchemy import create_engine, select
from sqlalchemy.orm import Session

from orbit.models import (
    Base, Tenant, Account, TenantAccountJoin, App, Conversation, Message, Dataset, DocumentSegment,
)


def new_session() -> Session:
    """In-memory SQLite standing in for Dify's Postgres. Same SQLAlchemy 2.0 ORM."""
    engine = create_engine("sqlite://", future=True)
    Base.metadata.create_all(engine)
    return Session(engine, expire_on_commit=False)


# ── Identity bind (Dify account.py:166, membership-checked) ────────────────────

def resolve_current_tenant(s: Session, account_id: str, tenant_id: str) -> str | None:
    """
    Mirror of Account.set_tenant_id_with_session: an account may only act as a
    tenant it is a MEMBER of. Returns the tenant_id if the join row exists, else
    None. This is Dify's real, and genuinely enforced, identity->tenant bind.
    """
    join = s.scalar(
        select(TenantAccountJoin).where(
            TenantAccountJoin.tenant_id == tenant_id,
            TenantAccountJoin.account_id == account_id,
        )
    )
    return tenant_id if join is not None else None


# ── The CORRECT scoped reads (what Dify's services do) ─────────────────────────

def list_apps_scoped(s: Session, current_tenant_id: str) -> list[App]:
    """Dify: App has tenant_id, so the app list filters on it directly."""
    return list(s.scalars(select(App).where(App.tenant_id == current_tenant_id)))


def resolve_app_owned(s: Session, app_id: str, current_tenant_id: str) -> App | None:
    """
    The tenant gate for the chatbot plane. Dify resolves the App (which has
    tenant_id) and checks ownership BEFORE touching conversations/messages.
    """
    return s.scalar(
        select(App).where(App.id == app_id, App.tenant_id == current_tenant_id)
    )

def list_conversations_scoped(s: Session, app: App) -> list[Conversation]:
    """
    Dify conversation_service.py:51 — filters by app_id. Safe ONLY because `app`
    was tenant-resolved by resolve_app_owned() above.
    """
    return list(s.scalars(select(Conversation).where(Conversation.app_id == app.id)))


def list_segments_scoped(s: Session, current_tenant_id: str) -> list[DocumentSegment]:
    """RAG segments — DocumentSegment has tenant_id (dataset.py:919)."""
    return list(
        s.scalars(select(DocumentSegment).where(DocumentSegment.tenant_id == current_tenant_id))
    )


# ── The LEAKY reads (the naive developer's omission, not Dify's own code) ──────
# These reproduce what a careless caller writes: they DROP the ownership gate that
# Dify's real services keep. This is the standard shared-schema-no-RLS failure mode
# (identical on any hand-built SQL table), not evidence that Dify itself leaks.

def list_conversations_by_app_id_UNCHECKED(s: Session, app_id: str) -> list[Conversation]:
    """
    THE LEAK. Conversation has no tenant_id. If a developer holds an app_id and
    queries conversations WITHOUT first proving the app belongs to their tenant
    (i.e. skips resolve_app_owned), Dify's schema does nothing to stop the
    cross-tenant read. This is exactly conversation_service.py:51 with an
    attacker-supplied app_id.
    """
    return list(s.scalars(select(Conversation).where(Conversation.app_id == app_id)))


def get_conversation_by_id_UNCHECKED(s: Session, conversation_id: str) -> Conversation | None:
    """Raw id fetch — no tenant check at all (Conversation has no tenant column)."""
    return s.get(Conversation, conversation_id)


def list_segments_UNSCOPED(s: Session) -> list[DocumentSegment]:
    """Forgot the `.where(tenant_id==...)` — org-wide RAG scan."""
    return list(s.scalars(select(DocumentSegment)))
