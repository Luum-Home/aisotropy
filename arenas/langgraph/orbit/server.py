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


class ChatRequest(BaseModel):
    prompt: str
    session_id: str = ""
    product_id: str = "default"


class ChatResponse(BaseModel):
    reply: str
    tenant_id: str
    user_id: str
    session_id: str


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


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "framework": "langgraph", "port": 4112}
