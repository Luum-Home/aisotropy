"""
Orbit data model — a FAITHFUL, SOURCE-GROUNDED transcription of Dify's real
multi-tenant schema, using Dify's real ORM (SQLAlchemy 2.0).

SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md
  Re-verified at langgenius/dify HEAD 4213e6b1 (2026-08-04): Conversation
  (model.py:1162) and Message (model.py:1535) still carry app_id and NO tenant_id;
  App.tenant_id at model.py:421; Dataset/DocumentSegment tenant_id still at
  dataset.py:180 / :919. Still a multi-container platform with no embeddable library.

This is a *labelled behavioral simulation* of Dify's persistence layer. It is NOT
a running Dify server (Dify is a Flask + Celery + Postgres + Redis platform that
cannot boot in this offline env — see README). Every table / column below is a
direct transcription of the genuine Dify source at HEAD
(langgenius/dify @ 1aea3460, 2026-07-16). The point of the exercise is to
reproduce Dify's *isolation contract* exactly, then show where it leaks.

Source anchors (real Dify files, transcribed from the pinned clone):
  - api/models/account.py:260  class Tenant(TypeBase)            __tablename__="tenants"
  - api/models/account.py:299  class TenantAccountJoin(TypeBase) tenant_id + account_id
  - api/models/account.py:166  Account.set_tenant_id_with_session -> membership-checked bind
  - api/models/model.py:402    class App(Base)   tenant_id: Mapped[str]  (app_tenant_id_idx)
  - api/models/model.py:1141   class Conversation(Base)  app_id ONLY — *no tenant_id column*
  - api/models/model.py:1506   class Message(Base)       app_id + conversation_id, no tenant_id
  - api/models/dataset.py:180  class Dataset(Base)       tenant_id: Mapped[str]
  - api/models/dataset.py:919  class DocumentSegment     tenant_id: Mapped[str]

PROVENANCE / HONESTY (read this): the `api/<path>:<line>` anchors are transcribed
from the pinned clone `1aea3460` (2026-07-16). In the honesty pass, the STRUCTURAL
claims below were independently re-verified against Dify's live source
(langgenius/dify): `Conversation`/`Message` carry app_id but NO tenant_id, `App`
carries tenant_id, `Account.set_tenant_id_with_session` membership-checks a
`TenantAccountJoin` row, and no `product_id` column exists — all confirmed. The
exact LINE NUMBERS at that commit were NOT re-checked in this pass and may have
drifted; where two files disagreed they were reconciled to a single value. Treat
the line offsets as approximate pointers into the pinned clone, not as re-verified.

THE CRUX (structurally re-verified): Dify uses the *shared-schema,
discriminator-column* pattern.
`tenant_id` is a plain column on top-level rows (App, Dataset, Document,
DocumentSegment). There is NO Postgres Row-Level-Security, NO schema-per-tenant
(grep for `CREATE POLICY` / `ENABLE ROW` across the whole api/ tree = 0 hits).
Isolation is therefore 100% *application-layer*: every service query must carry
`.where(Model.tenant_id == current_tenant_id)` by hand. Conversation/Message do
not even have a tenant_id — they inherit tenant scope transitively through
`app_id`, so a read that skips the app-ownership gate the developer is expected to
write can return another tenant's rows.

NOT A DIFY DEFECT — the standard shared-schema tradeoff. This is correct when the
developer writes the ownership gate and cross-reads when they skip it. Dify's OWN
services DO write it (conversation_service.py resolves the app under the tenant
first). The `_UNCHECKED` / `_UNSCOPED` reads in orbit/store.py are the *naive
developer's omission*, reproduced against this transcribed contract — the identical
same-key/wrong-key failure mode of ANY shared-schema-no-RLS store, including a bare
hand-built SQL table or aisotropy's own BadgerDB if you skipped the Scope filter.
What differs across frameworks is only whether the store makes the unscoped read
*unrepresentable* (aisotropy's ctx-propagated Scope) or merely *available*
(shared-schema `.where()`). Dify is in the second, mainstream, camp.
"""

from __future__ import annotations

import uuid

from sqlalchemy import ForeignKey, String, Index
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


def _uuid() -> str:
    return str(uuid.uuid4())


class Base(DeclarativeBase):
    pass


# ── Identity plane (Dify api/models/account.py) ────────────────────────────────

class Tenant(Base):
    """Dify: one tenant == one workspace (LICENSE §1a). account.py:260."""
    __tablename__ = "tenants"
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    name: Mapped[str] = mapped_column(String(255))


class Account(Base):
    """Dify api/models/account.py:89."""
    __tablename__ = "accounts"
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    name: Mapped[str] = mapped_column(String(255))


class TenantAccountJoin(Base):
    """
    Dify api/models/account.py:299 — the ONLY thing that binds an account to a
    tenant. Account.set_tenant_id_with_session (account.py:166) verifies a row
    exists here before it will set current_tenant. This is a REAL membership
    bind (stronger than Letta) — but it only gates *which tenant you may act as*,
    not whether an individual data query remembers to filter by it.
    """
    __tablename__ = "tenant_account_joins"
    __table_args__ = (Index("tenant_account_join_tenant_id_idx", "tenant_id"),)
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    tenant_id: Mapped[str] = mapped_column(String(36), ForeignKey("tenants.id"))
    account_id: Mapped[str] = mapped_column(String(36), ForeignKey("accounts.id"))
    role: Mapped[str] = mapped_column(String(16), default="owner")


# ── Application plane (Dify api/models/model.py) ───────────────────────────────

class App(Base):
    """
    Dify api/models/model.py:402. tenant_id IS a column here (app_tenant_id_idx).
    This is the top-level tenant boundary for the chatbot plane.
    """
    __tablename__ = "apps"
    __table_args__ = (Index("app_tenant_id_idx", "tenant_id"),)
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    tenant_id: Mapped[str] = mapped_column(String(36), ForeignKey("tenants.id"))
    name: Mapped[str] = mapped_column(String(255))
    mode: Mapped[str] = mapped_column(String(255), default="chat")  # AppMode.CHAT


class Conversation(Base):
    """
    Dify api/models/model.py:1141. NOTE: keyed on app_id ONLY. There is NO
    tenant_id column (verified — every index is app_id-first). Tenant scope is
    purely transitive: you must have already proven the app belongs to your
    tenant. conversation_service.py:51 filters `.where(Conversation.app_id ==
    app_model.id)` and trusts that app_model was tenant-resolved upstream.
    """
    __tablename__ = "conversations"
    __table_args__ = (Index("conversation_app_from_user_idx", "app_id", "from_end_user_id"),)
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    app_id: Mapped[str] = mapped_column(String(36), ForeignKey("apps.id"))
    from_end_user_id: Mapped[str] = mapped_column(String(255))
    name: Mapped[str] = mapped_column(String(255), default="New conversation")


class Message(Base):
    """Dify api/models/model.py:1506. Again app_id + conversation_id, no tenant_id."""
    __tablename__ = "messages"
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    app_id: Mapped[str] = mapped_column(String(36), ForeignKey("apps.id"))
    conversation_id: Mapped[str] = mapped_column(String(36), ForeignKey("conversations.id"))
    query: Mapped[str] = mapped_column(String(4096), default="")
    answer: Mapped[str] = mapped_column(String(8192), default="")


# ── Knowledge / RAG plane (Dify api/models/dataset.py) ─────────────────────────

class Dataset(Base):
    """Dify api/models/dataset.py:180. tenant_id IS a column here."""
    __tablename__ = "datasets"
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    tenant_id: Mapped[str] = mapped_column(String(36), ForeignKey("tenants.id"))
    name: Mapped[str] = mapped_column(String(255))


class DocumentSegment(Base):
    """
    Dify api/models/dataset.py:919. tenant_id column present. But the vector
    plane (core/rag/datasource/vdb) filters retrieval by *dataset_id* into a
    per-dataset collection (vector_factory.py:38 class_prefix=collection_name);
    tenant scope there is transitive through Dataset.tenant_id, not a filter on
    the HNSW scan itself.
    """
    __tablename__ = "document_segments"
    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_uuid)
    tenant_id: Mapped[str] = mapped_column(String(36), ForeignKey("tenants.id"))
    dataset_id: Mapped[str] = mapped_column(String(36), ForeignKey("datasets.id"))
    content: Mapped[str] = mapped_column(String(8192), default="")
