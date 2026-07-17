"""
FastAPI orbit server for LangGraph arena — mirrors the aisotropy HTTP surface.

Provides the same /v1/tenant/{tenant_id}/user/{user_id}/chat/stream route
so you can test the live serve path alongside the offline smoke test.

Port: 4112
"""

import uuid
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

# LangGraph's real store primitive — the same one scripts/smoke.py exercises
# offline. Serving it over HTTP lets the containerized smoke drive the isolation
# assertion against a real process, not an in-test object.
from langgraph.store.memory import InMemoryStore

# Fixed org root for the arena; identity is (org, tenant_id, user_id). The server
# binds the namespace to the PATH identity on every op (the @auth.on.store()
# analogue / aisotropy Scope), so a caller can neither write outside its own
# namespace nor widen a search past it. One process-lifetime store.
_ORG = "arena"
_STORE = InMemoryStore()


class ChatRequest(BaseModel):
    prompt: str
    session_id: str = ""
    product_id: str = "default"


class ChatResponse(BaseModel):
    reply: str
    tenant_id: str
    user_id: str
    session_id: str


class MemoryWrite(BaseModel):
    key: str
    content: str


class MemoryWriteResponse(BaseModel):
    stored: bool
    namespace: list[str]
    key: str


class MemorySearchResponse(BaseModel):
    tenant_id: str
    user_id: str
    results: list[dict]


# ── App ────────────────────────────────────────────────────────────────────

def get_lifespan():
    """Simple lifespan context manager."""
    async def startup():
        print(f"[langgraph orbit] Serving on :4112")

    async def shutdown():
        pass

    @asynccontextmanager
    async def context(app: FastAPI):
        await startup()
        yield
        await shutdown()

    return context


app = FastAPI(title="LangGraph Arena", lifespan=get_lifespan())


@app.post("/v1/tenant/{tenant_id}/user/{user_id}/chat", response_model=ChatResponse)
async def chat(tenant_id: str, user_id: str, req: ChatRequest):
    """Non-streaming chat endpoint — stub response for testing."""
    session_id = req.session_id or str(uuid.uuid4())[:8]

    return ChatResponse(
        reply=f"I am a LangGraph agent. Tenant: {tenant_id}, User: {user_id}",
        tenant_id=tenant_id,
        user_id=user_id,
        session_id=session_id,
    )


@app.post("/v1/tenant/{tenant_id}/user/{user_id}/chat/stream")
async def chat_stream(tenant_id: str, user_id: str, req: ChatRequest):
    """SSE streaming endpoint — stub response for testing."""
    session_id = req.session_id or str(uuid.uuid4())[:8]

    async def event_stream():
        yield f'data: {{"type": "start", "tenant_id": "{tenant_id}"}}\n\n'
        yield f'data: {{"type": "message", "reply": "I am a LangGraph agent."}}\n\n'
        yield f'data: {{"type": "done"}}\n\n'

    return StreamingResponse(event_stream(), media_type="text/event-stream")


# ── Tenant-scoped memory (identity-bound store, the "correct posture") ───────
#
# Both endpoints derive the namespace from the PATH identity (tenant_id/user_id),
# never from the request body or a caller-supplied prefix. Writes land only in
# the caller's own namespace; searches are rewritten to it. This is the
# @auth.on.store()-style binding from scripts/smoke.py PART B, wired into the
# serve path so a cross-tenant read is structurally impossible over HTTP.

@app.post(
    "/v1/tenant/{tenant_id}/user/{user_id}/memory",
    response_model=MemoryWriteResponse,
)
async def memory_write(tenant_id: str, user_id: str, req: MemoryWrite):
    namespace = (_ORG, tenant_id, user_id)
    _STORE.put(namespace, req.key, {"content": req.content})
    return MemoryWriteResponse(stored=True, namespace=list(namespace), key=req.key)


@app.get(
    "/v1/tenant/{tenant_id}/user/{user_id}/memory/search",
    response_model=MemorySearchResponse,
)
async def memory_search(tenant_id: str, user_id: str):
    # Identity wins: the search is bound to the caller's own namespace, so a
    # shallow-prefix scan across tenants cannot happen through this endpoint.
    identity = (_ORG, tenant_id, user_id)
    items = _STORE.search(identity)
    results = [
        {"namespace": list(i.namespace), "content": i.value.get("content", "")}
        for i in items
    ]
    return MemorySearchResponse(tenant_id=tenant_id, user_id=user_id, results=results)


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "framework": "langgraph", "port": 4112}
