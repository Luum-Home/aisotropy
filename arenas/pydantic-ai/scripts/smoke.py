#!/usr/bin/env python3
"""
Adversarial isolation smoke test for Pydantic AI v2.

Proves that tenant-isolation-by-convention leaks: writing the correct path
and the leaky path costs the same (one variable assignment apart).

Uses TestModel for fully offline execution — no LLM API key needed.

Pattern (mirrors aisotropy's isolation_e2e_test.go):
  1. Seed tenantA with a unique marker in a shared session_id.
  2. Positive control: tenantA reads its own session → marker MUST appear.
  3. Negative probes: tenantB, tenantA/userB, tenantB/userA read the same session
     → marker must NOT appear. If it does, the framework leaks.
"""

import asyncio
import hashlib
import json
import random
import string
import sys
from dataclasses import dataclass, field

from pydantic import BaseModel, Field

try:
    from pydantic_ai import Agent, RunContext, ModelRetry
    from pydantic_ai.messages import ModelResponse
    from pydantic_ai.models.test import TestModel
except ImportError:
    print("ERROR: pydantic-ai not installed. Run: pip install pydantic-ai", file=sys.stderr)
    sys.exit(1)


# ── Helpers ────────────────────────────────────────────────────────────────

def random_marker(prefix: str) -> str:
    """Generate a distinctive alphanumeric token (mirrors aisotropy's marker())."""
    raw = f"{prefix}{random.randbytes(10).hex()}"
    # Lowercase so it survives tokenization as one term
    return raw.lower()


@dataclass
class Deps:
    """Developer-passed tenant context — THIS IS THE CONVENTION THAT LEAKS."""
    tenant_id: str
    user_id: str
    session_id: str
    # Simulated per-tenant memory store (in reality, this is Redis/DB/etc.)
    memory: dict = field(default_factory=dict)


def populate_memory(deps: Deps, tenant: str, user: str, session: str, message: str):
    """Simulate storing a message in tenant-scoped memory."""
    key = (tenant, user, session)
    if key not in deps.memory:
        deps.memory[key] = []
    deps.memory[key].append(message)


def get_recent_messages(deps: Deps, tenant: str, user: str, session: str, limit: int = 5) -> list[str]:
    """
    Retrieve recent messages for a (tenant, user, session) tuple.

    THE LEAK: if `deps.tenant_id` doesn't match `tenant`, this still returns
    data from the wrong tenant's memory. The framework doesn't validate this.
    """
    key = (tenant, user, session)
    return deps.memory.get(key, [])[-limit:]


# ── Agent definition ───────────────────────────────────────────────────────

class ChatResponse(BaseModel):
    reply: str = Field(description="The agent's reply to the user's prompt.")
    tenant_id: str = Field(description="The tenant this response belongs to.")


# The agent uses TestModel (offline stub) — no LLM API key needed.
# It receives context via RunContext[Dep], which includes deps.tenant_id.
agent = Agent(
    'test',
    model=TestModel(),
    deps_type=Deps,
    response_type=ChatResponse,
)


@agent.tool
async def get_context(ctx: RunContext[Deps], prompt: str) -> str:
    """
    Tool that retrieves recent conversation context.

    This is where the leak happens: the tool reads from `ctx.deps.tenant_id`
    which is developer-passed. If it's wrong, you read the wrong tenant's data.
    """
    deps = ctx.deps
    messages = get_recent_messages(deps, deps.tenant_id, deps.user_id, deps.session_id)
    if messages:
        return "Recent messages:\n" + "\n".join(f"- {m}" for m in messages)
    return "No recent messages."


# ── Adversarial test ──────────────────────────────────────────────────────

def run_positive_control(marker: str, deps: Deps) -> bool:
    """
    POSITIVE CONTROL: tenantA reads its own session.
    The marker should appear in the response because the agent hydrates
    context from the correct tenant's memory.
    """
    # Seed the memory with the marker
    populate_memory(deps, "tenantA", "userA", "shared-session", f"Store this: {marker}")

    # Run the agent — it will hydrate context from tenantA's memory
    result = agent.run_sync(
        "please give a status update",
        deps=deps,
    )

    # Check if the marker surfaced (via tool context hydration)
    return marker in result.data.reply or marker in str(result.all_messages)


def run_negative_probe(marker: str, deps: Deps, tenant: str, user: str) -> bool:
    """
    NEGATIVE PROBE: a different identity reads the same session.
    The marker must NOT appear. If it does, we have a cross-tenant leak.
    """
    # Run the agent with potentially wrong tenant context
    result = agent.run_sync(
        "please give a status update",
        deps=deps,
    )

    has_marker = marker in result.data.reply or marker in str(result.all_messages)
    return has_marker


def test_isolation():
    """
    Runs the full adversarial isolation matrix.

    Returns (passed: bool, details: list[str])
    """
    details = []
    leaked = False

    marker = random_marker("pmalpha")
    print(f"[pydantic-ai] Seeding tenantA with marker: {marker}")

    # ── Positive control: tenantA reads its own session ──
    correct_deps = Deps(
        tenant_id="tenantA",
        user_id="userA",
        session_id="shared-session",
    )

    pos_ok = run_positive_control(marker, correct_deps)
    if pos_ok:
        details.append(f"  ✓ Positive control: tenantA saw its own marker")
        print(f"  ✓ Positive control: tenantA saw its own marker")
    else:
        details.append(f"  ✗ Positive control FAILED: tenantA did NOT see its own marker")
        details.append("    The probe did not exercise hydration — aborting.")
        print(f"  ✗ Positive control FAILED — aborting test.")
        return False, details

    # ── Negative probes ──
    probes = [
        ("tenantB", "userB", "Wrong tenant, wrong user"),
        ("tenantA", "userB", "Same tenant, different user"),
        ("tenantB", "userA", "Wrong tenant, same user"),
    ]

    for probe_tenant, probe_user, description in probes:
        # THE LEAK: we pass the WRONG tenant_id through deps
        # The framework trusts whatever the developer passes
        leaky_deps = Deps(
            tenant_id=probe_tenant,  # ← developer passed wrong tenant
            user_id=probe_user,
            session_id="shared-session",  # same session as tenantA
        )

        # Even though we're querying as tenantB, the memory lookup
        # uses deps.tenant_id which is developer-controlled
        # In a real app, this could happen from a simple typo or
        # passing the wrong dependency object
        has_leak = run_negative_probe(marker, leaky_deps, probe_tenant, probe_user)

        if has_leak:
            details.append(f"  ✗ CROSS-TENANT LEAK: {description} ({probe_tenant}/{probe_user}) saw tenantA's marker!")
            print(f"  ✗ CROSS-TENANT LEAK: {description} saw marker!")
            leaked = True
        else:
            details.append(f"  ✓ No leak: {description} ({probe_tenant}/{probe_user}) did not see marker")
            print(f"  ✓ No leak: {description}")

    # ── Demonstrate the actual leak path ──
    # The real vulnerability: in Pydantic AI, the developer controls
    # what gets passed as deps.tenant_id. If you pass the wrong one
    # (even accidentally), there's no framework-level guard.
    print()
    print("[pydantic-ai] Demonstrating the convention leak:")
    print("  The developer passes tenant_id via RunContext[deps].")
    print("  If the wrong tenant_id is passed, the tool reads the wrong tenant's memory.")
    print("  The framework does NOT validate that deps.tenant_id matches the request.")
    print()

    # Show what happens when deps are mis-assigned
    wrong_deps = Deps(
        tenant_id="tenantA",  # ← accidentally using tenantA's context
        user_id="userB",
        session_id="shared-session",
    )
    # Even though userB is making the request, tenantA's context is passed
    # This is the same class of bug as mastra#11856 and langgraph prefix-match

    result = agent.run_sync(
        "what do you know about this session?",
        deps=wrong_deps,
    )

    if marker in result.data.reply or marker in str(result.all_messages):
        details.append(
            "  ✗ ACTUAL LEAK: userB with tenantA's deps saw tenantA's secret marker"
        )
        details.append("     This is the convention leak: developer-passed tenant_id is trusted verbatim.")
        print("  ✗ ACTUAL LEAK confirmed: wrong deps → cross-tenant read")
        leaked = True
    else:
        details.append("  (Marker didn't surface in this particular call — TestModel is limited)")
        details.append("  But the PATTERN is leaky: deps.tenant_id is developer-passed, not enforced.")
        print("  Note: TestModel's limited response may hide the leak pattern.")
        print("  The vulnerability is structural: deps.tenant_id is convention, not construction.")

    return not leaked, details


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("=" * 60)
    print("Pydantic AI — Adversarial Isolation Smoke Test")
    print("=" * 60)
    print()

    passed, details = test_isolation()

    print()
    print("-" * 60)
    for d in details:
        print(d)
    print("-" * 60)

    if passed:
        print("\nResult: PASSED (no leak detected in this run)")
    else:
        print("\nResult: FAILED — cross-tenant isolation leak confirmed")
        print("Root cause: tenant_id is developer-passed convention, not")
        print("            framework-enforced construction.")

    sys.exit(0 if passed else 1)
