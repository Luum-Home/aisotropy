#!/usr/bin/env python3
"""
Isolation-model smoke test for Pydantic AI v2.

Pydantic AI is a STATELESS agent LIBRARY. By design it ships NO memory store and
NO multi-tenancy layer — "Agents are stateless and designed to be global", "no
built-in persistent memory, your agent starts from scratch every run" (official
docs: https://ai.pydantic.dev/dependencies/, https://ai.pydantic.dev/message-history/).
`RunContext[deps]` is pure dependency injection: `deps` is a plain object the
DEVELOPER constructs and the framework threads through tools untouched. It is
bound to nothing — no principal, no auth, no tenant concept.

SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (framework matrix
places Pydantic AI at L0 on the L0-L3 ladder).

So there is no framework isolation boundary to test. Any scoped memory is 100%
BYO: your own dict / Redis / DB, keyed by a key YOU compute. This script makes
that concrete. The "store" here is a 15-line plain-Python dict keyed by
`(tenant_id, user_id, session_id)`. That dict is BOTH the boundary and the only
thing that can get the key wrong — the framework isn't in the path at all.

What this demonstrates (a classification, NOT a defect):
  1. Seed one slice with a unique marker.
  2. POSITIVE CONTROL: the correct key reads its own marker back.
  3. CORRECT SCOPING: a different, correct key reads its own (empty) slice — no
     marker. The exact-key dict partitions fine WHEN THE KEY IS RIGHT.
  4. NO ISOLATION LAYER: a request that should be scoped to tenant B, served with
     a key the developer computed for tenant A, reads tenant A's slice. Nothing
     objects — because the framework has no concept of tenant/user/session to
     object with. This is true of ANY KV store (including aisotropy's BadgerDB):
     pass the wrong key, get the wrong slice. The distinction is WHO owns key
     computation. Here it is 100% the developer (DIY). aisotropy computes and
     binds all axes at the request boundary and propagates them via
     `context.Context`, so its DEFAULT read path is already scoped.

This is an out-of-scope / DIY classification for the framework, not a leak in it.
A leak would mean a framework boundary failed; here the arena's own dict is the
boundary. The finding is: no isolation LAYER exists — it is yours to build.
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
    """Generate a distinctive alphanumeric token."""
    return f"{prefix}{random.randbytes(10).hex()}".lower()


@dataclass
class Deps:
    """Developer-constructed DI context — the developer owns every field.

    The lookup key comes from these fields. The framework does not (cannot)
    validate that they match any caller: it has no auth or tenant concept. This
    is dependency injection, nothing more.
    """
    tenant_id: str
    user_id: str
    session_id: str
    # BYO memory store (in reality Redis/DB/etc.) — the framework ships none.
    memory: dict = field(default_factory=dict)


def populate_memory(deps: Deps, tenant: str, user: str, session: str, message: str):
    """Store a message in the DIY exact-key store."""
    deps.memory.setdefault((tenant, user, session), []).append(message)


def get_recent_messages(deps: Deps, limit: int = 5) -> list[str]:
    """Retrieve recent messages using the DEVELOPER-COMPUTED key on `deps`.

    This is exactly what the `get_context` tool below calls. The lookup key is
    `(deps.tenant_id, deps.user_id, deps.session_id)` — whatever the developer
    put in deps. Right key -> right slice; wrong key -> wrong slice. That is the
    behavior of a plain dict, and the framework adds no guard on top because it
    has no tenant model to guard with.
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
    """Retrieves recent conversation context via the developer-computed deps key."""
    messages = get_recent_messages(ctx.deps)
    if messages:
        return "Recent messages:\n" + "\n".join(f"- {m}" for m in messages)
    return "No recent messages."


def framework_wiring_ok(shared_memory: dict) -> bool:
    """Prove the installed Pydantic AI API boots: DI + structured output.

    Independent of the store check below — this exercises the actual framework
    (Agent construction, RunContext DI, typed output) so "the library works" is
    demonstrated, not assumed. It does NOT touch the isolation question, which is
    a DIY-store property, not a framework property.
    """
    try:
        deps = Deps(tenant_id="tenantA", user_id="userA",
                    session_id="shared-session", memory=shared_memory)
        result = agent.run_sync("please give a status update", deps=deps)
        return isinstance(result.output, ChatResponse)  # installed API: result.output
    except Exception as exc:  # pragma: no cover - defensive
        print(f"  ! framework wiring probe raised: {exc}")
        return False


# ── Isolation-model demonstration ───────────────────────────────────────────

def test_isolation():
    """Returns (passed: bool, details: list[str]).

    passed == True means: the DIY store partitions correctly when the key is
    right, AND a wrong developer-computed key reads the wrong slice (because no
    framework isolation layer exists to prevent it).
    """
    details = []

    marker = random_marker("pmalpha")
    print(f"[pydantic-ai] Seeding tenantA with marker: {marker}")

    # One DIY store (dict). Developer-computed keys select the slice.
    shared_memory: dict = {}

    # Seed ONLY tenantA/userA/shared-session with the marker.
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

    # Framework wiring probe (independent): proves the installed API constructs and runs.
    if framework_wiring_ok(shared_memory):
        details.append("  [PASS] Framework wiring: Agent(model=, output_type=) + RunContext DI runs, result.output typed")
        print("  [PASS] Framework wiring: Agent + RunContext DI runs (result.output typed)")
    else:
        details.append("  [WARN] Framework wiring probe did not return a typed output (store-level check is independent)")
        print("  [WARN] Framework wiring probe inconclusive (store-level check is independent)")

    # ── 2. CORRECT SCOPING: tenantB reads with its OWN key -> no marker ──
    tenantB_correct = Deps("tenantB", "userB", "shared-session", memory=shared_memory)
    b_correct_present = marker in " ".join(get_recent_messages(tenantB_correct))
    if not b_correct_present:
        details.append("  [PASS] Correct scoping: tenantB with its own key saw NO marker (exact-key store partitions when the key is right)")
        print("  [PASS] Correct scoping: tenantB with correct key saw no marker")
    else:
        details.append("  [FAIL] Unexpected: tenantB with its own correct key saw tenantA's marker (the DIY store is broken)")
        print("  [FAIL] Unexpected: correct key returned the wrong slice")
        return False, details

    # ── 3. NO ISOLATION LAYER: tenantB request, developer computed tenantA's key ──
    # The caller is tenantB/userB, but deps was built with tenantA's key (stale/
    # copy-pasted deps, cached context, a one-line typo in the developer's own key
    # computation). The framework has NO tenant concept, so there is nothing to
    # reject it — the dict simply returns whatever key it is handed. This is a
    # property of every KV store, not a Pydantic AI defect.
    print()
    print("[pydantic-ai] No isolation layer exists — the developer owns key computation:")
    print("  Caller is tenantB/userB, but deps.tenant_id was computed as 'tenantA'.")
    print("  get_recent_messages() (what the get_context tool calls) uses deps verbatim;")
    print("  the framework has no tenant model to object with.")
    wrong_key = Deps(
        tenant_id="tenantA",   # <-- developer computed the wrong key for a tenantB request
        user_id="userA",
        session_id="shared-session",
        memory=shared_memory,
    )
    wrong_key_slice = marker in " ".join(get_recent_messages(wrong_key))
    if wrong_key_slice:
        details.append("  [DIY] No isolation layer: tenantB request + a tenantA key -> reads tenantA's slice")
        details.append("        Root cause: deps is developer-constructed DI; the framework binds no axis to any caller.")
        details.append("        Same as any KV store. aisotropy's differentiator: it computes+binds the key at the")
        details.append("        request boundary and propagates via context.Context, so its DEFAULT read is scoped.")
        # SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Pydantic AI = L0)
        print("  [DIY] Confirmed: a wrong developer-computed key reads the wrong slice — no framework layer to prevent it")
    else:
        details.append("  [FAIL] Expected the wrong key to read tenantA's slice — the DIY store did not behave like a plain dict")
        print("  [FAIL] Wrong key did not return tenantA's slice (unexpected)")
        return False, details

    return True, details


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("=" * 60)
    print("Pydantic AI — Isolation-Model Smoke Test")
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
        print("  The DIY store partitions when the key is right; a wrong developer-computed")
        # SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Pydantic AI = L0)
        print("  key reads the wrong slice because NO framework isolation layer exists.")
        print("  Classification: Pydantic AI is a stateless agent library that ships no")
        print("           memory or multi-tenancy layer. Isolation is 100% BYO/developer-")
        print("           managed — an out-of-scope/DIY classification, NOT a framework defect.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
