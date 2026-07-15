"""
FastAPI orbit app for Pydantic AI arena — mirrors the aisotropy HTTP surface.

Provides the same /v1/tenant/{tenant_id}/user/{user_id}/chat/stream route
so you can test the live serve path alongside the offline smoke test.

Port: 4110
"""

import uuid
from contextlib import asynccontextmanager
from dataclasses import dataclass, field

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field

try:
    from pydantic_ai import Agent, RunContext
    from pydantic_ai.models.stub import StubModel
except ImportError:
    StubModel = None


# ── Models ─────────────────────────────────────────────────────────────────

class ChatRequest(BaseModel):
    prompt: str
    session_id: str = ""
    product_id: str = "default"


class ChatResponse(BaseModel):
    reply: str
    tenant_id: str
    user_id: str
    session_id: str


@dataclass
class Deps:
    tenant_id: str
    user_id: str
    session_id: str
    memory: dict = field(default_factory=dict)


# ── Agent ──────────────────────────────────────────────────────────────────

agent = Agent(
    'stub',
    model=StubModel(text_response="I am a Pydantic AI agent."),
    deps_type=Deps,
)


@agent.tool
async def get_messages(ctx: RunContext[Deps], limit: int = 5) -> str:
    """Retrieve recent messages from the shared session."""
    messages = ctx.deps.memory.get(
        (ctx.deps.tenant_id, ctx.deps.user_id, ctx.deps.session_id), []
    )[-limit:]
    if messages:
        return "\n".join(messages)
    return "No recent messages."


# ── App ────────────────────────────────────────────────────────────────────

# In-memory store keyed by (tenant, user) — simulates a real DB
stores: dict[tuple[str, str], Deps] = {}


def get_store(tenant_id: str, user_id: str) -> Deps:
    key = (tenant_id, user_id)
    if key not in stores:
        stores[key] = Deps(tenant_id=tenant_id, user_id=user_id)
    return stores[key]


@asynccontextmanager
async def lifespan(app: FastAPI):
    print(f"[pydantic-ai orbit] Serving on :4110")
    yield


app = FastAPI(title="Pydantic AI Arena", lifespan=lifespan)


@app.post("/v1/tenant/{tenant_id}/user/{user_id}/chat", response_model=ChatResponse)
async def chat(tenant_id: str, user_id: str, req: ChatRequest):
    """Non-streaming chat endpoint."""
    store = get_store(tenant_id, user_id)
    store.session_id = req.session_id or str(uuid.uuid4())[:8]

    # Store the user's message
    msg = f"[{user_id}]: {req.prompt}"
    key = (tenant_id, user_id, store.session_id)
    if key not in store.memory:
        store.memory[key] = []
    store.memory[key].append(msg)

    # Run the agent
    result = agent.run_sync(req.prompt, deps=store)

    return ChatResponse(
        reply=result.data,
        tenant_id=tenant_id,
        user_id=user_id,
        session_id=store.session_id,
    )


@app.post("/v1/tenant/{tenant_id}/user/{user_id}/chat/stream")
async def chat_stream(tenant_id: str, user_id: str, req: ChatRequest):
    """SSE streaming endpoint."""
    store = get_store(tenant_id, user_id)
    store.session_id = req.session_id or str(uuid.uuid4())[:8]

    msg = f"[{user_id}]: {req.prompt}"
    key = (tenant_id, user_id, store.session_id)
    if key not in store.memory:
        store.memory[key] = []
    store.memory[key].append(msg)

    async def event_stream():
        yield f"data: {{\"type\": \"start\", \"tenant_id\": \"{tenant_id}\"}}\n\n"
        result = agent.run_sync(req.prompt, deps=store)
        yield f"data: {{\"type\": \"message\", \"reply\": \"{result.data}\"}}\n\n"
        yield f"data: {{\"type\": \"done\"}}\n\n"

    return StreamingResponse(event_stream(), media_type="text/event-stream")


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "framework": "pydantic-ai", "port": 4110}
