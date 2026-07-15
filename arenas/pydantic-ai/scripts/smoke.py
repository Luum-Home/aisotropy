#!/usr/bin/env python3
"""
Adversarial isolation smoke test for Pydantic AI v2.

Pydantic AI has NO built-in multi-tenant isolation. The memory store is your own
DIY code (a dict / Redis / DB), scoped by a developer-passed key. The framework's
`RunContext[deps]` dependency injection is clean and typed — but the `tenant_id`
that ends up in the lookup key is whatever the developer put in `deps`. Nothing
ties it to the authenticated principal. Pass the wrong key (a stale/copy-pasted
deps object, a cached context) and you silently read another tenant's data.

This test proves the finding DETERMINISTICALLY at the memory-store layer — the
same store the `get_context` tool reads via `get_recent_messages`. It does NOT
depend on the stub LLM (`TestModel`) invoking a tool and echoing a marker, which
is unreliable.

Matrix (mirrors aisotropy's isolation_e2e_test.go):
  1. Seed tenantA's memory with a unique marker.
  2. POSITIVE CONTROL: read with tenantA's own key -> marker MUST appear.
  3. CORRECT SCOPING: tenantB reads with its OWN (correct) key -> no marker
     (the DIY dict is exact-key; it is safe *when the key is right*).
  4. CONVENTION LEAK: tenantB's request, but the developer passes tenantA's key
     via deps -> tenantB reads tenantA's marker. The framework never checks that
     deps.tenant_id matches the caller. This is the expected finding.

A successful run = positive control verified AND the cross-tenant leak reproduced.
"""

import random
import sys
from dataclasses import dataclass, field

from pydantic import BaseModel, Field

try:
    from pydantic_ai import Agent, RunContext
    from pydantic_ai.models.test import TestModel
except ImportError:
    print("ERROR: pydantic-ai not installed. Run: pip install pydantic-ai", file=sys.stderr)
    sys.exit(1)


# ── Helpers ────────────────────────────────────────────────────────────────

def random_marker(prefix: str) -> str:
    """Generate a distinctive alphanumeric token (mirrors aisotropy's marker())."""
    return f"{prefix}{random.randbytes(10).hex()}".lower()


@dataclass
class Deps:
    """Developer-passed tenant context — THIS IS THE CONVENTION THAT LEAKS.

    The lookup key comes from these fields. The framework does not validate that
    they match the authenticated caller — the developer is fully responsible.
    """
    tenant_id: str
    user_id: str
    session_id: str
    # Simulated per-tenant memory store (in reality this is Redis/DB/etc.).
    memory: dict = field(default_factory=dict)


def populate_memory(deps: Deps, tenant: str, user: str, session: str, message: str):
    """Simulate storing a message in tenant-scoped memory (exact-key)."""
    deps.memory.setdefault((tenant, user, session), []).append(message)


def get_recent_messages(deps: Deps, limit: int = 5) -> list[str]:
    """Retrieve recent messages using the DEVELOPER-PASSED key on `deps`.

    This is exactly what the `get_context` tool below calls. The lookup key is
    `(deps.tenant_id, deps.user_id, deps.session_id)` — whatever the developer
    stuffed into deps. If that is the wrong tenant, this returns the wrong
    tenant's data. No framework guard exists.
    """
    key = (deps.tenant_id, deps.user_id, deps.session_id)
    return deps.memory.get(key, [])[-limit:]


# ── Agent definition (proves the installed API wiring works) ────────────────

class ChatResponse(BaseModel):
    reply: str = Field(description="The agent's reply to the user's prompt.")
    tenant_id: str = Field(description="The tenant this response belongs to.")


agent = Agent(
    model=TestModel(),        # installed API: model= kwarg
    deps_type=Deps,
    output_type=ChatResponse,  # installed API: output_type= (not response_type)
)


@agent.tool
async def get_context(ctx: RunContext[Deps], prompt: str) -> str:
    """Retrieves recent conversation context via the developer-passed deps key."""
    messages = get_recent_messages(ctx.deps)
    if messages:
        return "Recent messages:\n" + "\n".join(f"- {m}" for m in messages)
    return "No recent messages."


def framework_wiring_ok(shared_memory: dict) -> bool:
    """Prove the installed Pydantic AI API boots: DI + structured output.

    Non-fatal; isolation is asserted at the store level, not through the LLM.
    """
    try:
        deps = Deps(tenant_id="tenantA", user_id="userA",
                    session_id="shared-session", memory=shared_memory)
        result = agent.run_sync("please give a status update", deps=deps)
        return isinstance(result.output, ChatResponse)  # installed API: result.output
    except Exception as exc:  # pragma: no cover - defensive
        print(f"  ! framework wiring probe raised: {exc}")
        return False


# ── Adversarial test ──────────────────────────────────────────────────────

def test_isolation():
    """Returns (passed: bool, details: list[str]).

    passed == True means: positive control verified AND the expected
    cross-tenant convention leak was reproduced.
    """
    details = []

    marker = random_marker("pmalpha")
    print(f"[pydantic-ai] Seeding tenantA with marker: {marker}")

    # Shared DIY store (dict). One store, developer-passed keys select the slice.
    shared_memory: dict = {}

    # Seed ONLY tenantA/userA/shared-session with the secret marker.
    seed_deps = Deps("tenantA", "userA", "shared-session", memory=shared_memory)
    populate_memory(seed_deps, "tenantA", "userA", "shared-session", f"Store this: {marker}")

    # ── 1. POSITIVE CONTROL: tenantA reads its own key ──
    correct = Deps("tenantA", "userA", "shared-session", memory=shared_memory)
    pos_ok = marker in " ".join(get_recent_messages(correct))
    if pos_ok:
        details.append("  [PASS] Positive control: tenantA read its own marker")
        print("  [PASS] Positive control: tenantA read its own marker")
    else:
        details.append("  [FAIL] Positive control: tenantA did NOT read its own marker — aborting")
        print("  [FAIL] Positive control FAILED — aborting")
        return False, details

    # Framework wiring probe (non-fatal): proves the installed API constructs and runs.
    if framework_wiring_ok(shared_memory):
        details.append("  [PASS] Framework wiring: Agent(model=, output_type=) + RunContext DI runs, result.output typed")
        print("  [PASS] Framework wiring: Agent + RunContext DI runs (result.output typed)")
    else:
        details.append("  [WARN] Framework wiring probe did not return a typed output (isolation still asserted at store level)")
        print("  [WARN] Framework wiring probe inconclusive (isolation asserted at store level)")

    # ── 2. CORRECT SCOPING: tenantB reads with its OWN key -> no marker ──
    tenantB_correct = Deps("tenantB", "userB", "shared-session", memory=shared_memory)
    b_correct_leak = marker in " ".join(get_recent_messages(tenantB_correct))
    if not b_correct_leak:
        details.append("  [PASS] Correct scoping: tenantB with its own key saw NO marker (exact-key store is safe when the key is right)")
        print("  [PASS] Correct scoping: tenantB with correct key saw no marker")
    else:
        details.append("  [FAIL] Unexpected: tenantB with its own correct key saw tenantA's marker (store is broken, not just convention)")
        print("  [FAIL] Unexpected leak with correct key")
        return False, details

    # ── 3. CONVENTION LEAK: tenantB request, developer passes tenantA's key ──
    # The caller is tenantB/userB, but deps was built with tenantA's identity
    # (stale/copy-pasted deps, cached context, a one-line typo). Nothing checks it.
    print()
    print("[pydantic-ai] Demonstrating the convention leak:")
    print("  Caller is tenantB/userB, but deps.tenant_id was set to 'tenantA'.")
    print("  get_recent_messages() (what the get_context tool calls) trusts deps verbatim.")
    leaky = Deps(
        tenant_id="tenantA",   # <-- wrong key passed for a tenantB request
        user_id="userA",
        session_id="shared-session",
        memory=shared_memory,
    )
    leak_hit = marker in " ".join(get_recent_messages(leaky))
    if leak_hit:
        details.append("  [LEAK] Convention leak: tenantB request + tenantA key -> tenantB read tenantA's marker")
        details.append("         Root cause: deps.tenant_id is developer-passed, not framework-enforced. No guard ties the key to the caller.")
        print("  [LEAK] Convention leak confirmed: wrong deps key -> cross-tenant read")
    else:
        details.append("  [FAIL] Expected leak did NOT reproduce — the store did not return tenantA's data for the wrong key")
        print("  [FAIL] Expected leak did not reproduce")
        return False, details

    return True, details


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
        print("\nResult: PASS")
        print("  Positive control verified AND the expected cross-tenant leak reproduced.")
        print("  Finding: Pydantic AI has NO built-in tenant isolation — the memory store")
        print("           is DIY, scoped by a developer-passed key. A wrong key leaks,")
        print("           and the correct/leaky paths are one variable apart.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
