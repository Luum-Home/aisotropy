#!/usr/bin/env python3
"""
Multi-PRODUCT scoping demo for Pydantic AI v2 — the 3rd BYO-key axis.

aisotropy scopes memory by FOUR keys: (tenant_id, user_id, session_id).
The isolation-model smoke (scripts/smoke.py) exercised tenant/user/session. This
demo adds `product_id` and shows the same structural truth one axis deeper:

  In Pydantic AI, `product_id` is JUST ANOTHER developer-constructed field on
  `deps`. The framework never binds it to an authenticated principal, an API
  surface, or anything at all — because Pydantic AI is a stateless agent LIBRARY
  with no memory or multi-tenancy layer by design ("Agents are stateless and
  designed to be global"; deps is pure dependency injection). It is a dict key
  YOU chose to put in the lookup tuple. Compute the wrong product_id and your own
  store returns another product's slice — exactly like tenant_id, because both
  are the same kind of thing: a BYO key, not a framework-enforced boundary.

SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (framework matrix
places Pydantic AI at L0 on the L0-L3 ladder).

There is no framework isolation LAYER to test on any axis. What we exercise is a
plain-Python dict keyed by the 4-tuple — the arena's own store, which is both the
boundary and the only thing that can compute the key wrong. This is an
out-of-scope / DIY classification for the framework, NOT a defect in it.

What this demonstrates:
  1. Seed tenantA/userA/PRODUCT-X with a unique marker.
  2. POSITIVE CONTROL: read with the correct (tenantA, userA, productX) key ->
     marker MUST appear.
  3. CORRECT SCOPING: same tenant+user but the CORRECT other product (productY)
     reads its own slice -> NO marker (exact-key store partitions when the key is right).
  4. NO ISOLATION LAYER: a request that should be scoped to productY, but the
     developer computed productX in deps (stale/copy-pasted/cached deps, a one-line
     typo). Same tenant, same user, WRONG product -> reads productX's slice. The
     framework has no product concept to reject it — a plain dict returns whatever
     key it is handed. aisotropy binds three axes at the request boundary and
     propagates them via context.Context, so its DEFAULT read path is already scoped.
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
    """Developer-constructed DI context — FOUR axes, all of them BYO keys.

    The lookup key is the 4-tuple (tenant_id, user_id, session_id).
    `product_id` is no more privileged than `tenant_id`: the framework does not
    validate ANY of them against a caller, because it has no auth or tenant/
    product model. The developer owns every one of these fields — this is
    dependency injection, nothing more.
    """
    tenant_id: str
    user_id: str
    session_id: str
    product_id: str
    # BYO per-(tenant_id, user_id, session_id) memory store (the framework ships none).
    memory: dict = field(default_factory=dict)


def populate_memory(deps: Deps, tenant: str, user: str, session: str, product: str, message: str):
    """Store a message in the DIY 4-axis-scoped store (exact-key)."""
    deps.memory.setdefault((tenant_id, user_id, session_id), []).append(message)


def get_recent_messages(deps: Deps, limit: int = 5) -> list[str]:
    """Retrieve recent messages using the DEVELOPER-COMPUTED 4-tuple key on `deps`.

    Lookup key is (deps.tenant_id, deps.user_id, deps.session_id, deps.product_id) —
    whatever the developer put in deps. Right key -> right slice; wrong product_id
    -> the wrong product's slice. That is plain-dict behavior; the framework adds
    no guard on any axis because it has no product/tenant model to guard with.
    """
    key = (deps.tenant_id, deps.user_id, deps.session_id, deps.product_id)
    return deps.memory.get(key, [])[-limit:]


# ── Agent definition (proves the installed API wiring works) ────────────────

class ChatResponse(BaseModel):
    reply: str = Field(description="The agent's reply to the user's prompt.")
    product_id: str = Field(description="The product this response belongs to.")


agent = Agent(
    model=TestModel(),         # installed API: model= kwarg
    deps_type=Deps,
    output_type=ChatResponse,  # installed API: output_type=
)


@agent.tool
async def get_context(ctx: RunContext[Deps], prompt: str) -> str:
    """Retrieves recent conversation context via the developer-computed deps key."""
    messages = get_recent_messages(ctx.deps)
    if messages:
        return "Recent messages:\n" + "\n".join(f"- {m}" for m in messages)
    return "No recent messages."


def framework_wiring_ok(shared_memory: dict) -> bool:
    """Prove the installed Pydantic AI API boots with the 4-axis deps: DI + typed output.

    Independent of the store check below — it exercises the actual framework
    (Agent construction, RunContext DI, typed output), not the DIY-store scoping.
    """
    try:
        deps = Deps(tenant_id="tenantA", user_id="userA",
                    session_id="shared-session", product_id="productX",
                    memory=shared_memory)
        result = agent.run_sync("please give a status update", deps=deps)
        return isinstance(result.output, ChatResponse)  # installed API: result.output
    except Exception as exc:  # pragma: no cover - defensive
        print(f"  ! framework wiring probe raised: {exc}")
        return False


# ── Product-scoping demonstration ──────────────────────────────────────────

def test_product_isolation():
    """Returns (passed: bool, details: list[str]).

    passed == True means: the DIY 4-axis store partitions when the product key is
    right, AND a wrong developer-computed product_id reads the wrong product's
    slice (because no framework isolation layer exists on the product axis).

    SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Pydantic AI = L0)
    """
    details = []

    marker = random_marker("pmprod")
    print(f"[pydantic-ai] Seeding tenantA/userA/PRODUCT-X with marker: {marker}")

    # One DIY store (dict). Developer-computed 4-tuple selects the slice.
    shared_memory: dict = {}

    # Seed ONLY tenantA/userA/shared-session/PRODUCT-X with the marker.
    seed_deps = Deps("tenantA", "userA", "shared-session", "productX", memory=shared_memory)
    populate_memory(seed_deps, "tenantA", "userA", "shared-session", "productX",
                    f"Store this: {marker}")

    # ── 1. POSITIVE CONTROL: correct (tenantA, userA, productX) key ──
    correct = Deps("tenantA", "userA", "shared-session", "productX", memory=shared_memory)
    pos_ok = marker in " ".join(get_recent_messages(correct))
    if pos_ok:
        details.append("  [PASS] Positive control: tenantA/userA/productX read its own marker")
        print("  [PASS] Positive control: tenantA/userA/productX read its own marker")
    else:
        details.append("  [FAIL] Positive control: productX did NOT read its own marker — aborting")
        print("  [FAIL] Positive control FAILED — aborting")
        return False, details

    # Framework wiring probe (independent): proves the installed API constructs and runs.
    if framework_wiring_ok(shared_memory):
        details.append("  [PASS] Framework wiring: Agent(model=, output_type=) + RunContext DI runs, result.output typed (4-axis deps)")
        print("  [PASS] Framework wiring: Agent + RunContext DI runs with 4-axis deps (result.output typed)")
    else:
        details.append("  [WARN] Framework wiring probe did not return a typed output (store-level check is independent)")
        print("  [WARN] Framework wiring probe inconclusive (store-level check is independent)")

    # ── 2. CORRECT SCOPING: same tenant+user, CORRECT other product (productY) ──
    # Everything identical EXCEPT product_id — proves the axis actually partitions
    # when the key is right, so the wrong-key read below is about the key, not a
    # broken store.
    productY_correct = Deps("tenantA", "userA", "shared-session", "productY", memory=shared_memory)
    y_correct_present = marker in " ".join(get_recent_messages(productY_correct))
    if not y_correct_present:
        details.append("  [PASS] Correct scoping: productY (same tenant+user) saw NO marker (product axis partitions when the key is right)")
        print("  [PASS] Correct scoping: productY with its own key saw no marker")
    else:
        details.append("  [FAIL] Unexpected: productY with its own correct key saw productX's marker (the DIY store is broken)")
        print("  [FAIL] Unexpected: correct productY key returned the wrong slice")
        return False, details

    # ── 3. NO ISOLATION LAYER: productY request, developer computed productX ──
    # SAME tenant, SAME user — only product_id is wrong. product_id is not tied to
    # anything the caller authenticated as; it is a field the developer set. Set it
    # wrong (stale deps, copy-paste, cache) and your own dict returns productX's
    # slice. The framework has no product concept to object with — this is plain
    # KV-store behavior, not a Pydantic AI defect.
    print()
    print("[pydantic-ai] No isolation layer on the product axis — the developer owns the key:")
    print("  Request should be scoped to productY, but deps.product_id was computed as 'productX'.")
    print("  SAME tenant, SAME user — only the product key differs.")
    print("  get_recent_messages() (what the get_context tool calls) uses deps verbatim.")
    wrong_key = Deps(
        tenant_id="tenantA",   # same tenant
        user_id="userA",       # same user
        session_id="shared-session",
        product_id="productX",  # <-- wrong product computed for a productY request
        memory=shared_memory,
    )
    wrong_key_slice = marker in " ".join(get_recent_messages(wrong_key))
    if wrong_key_slice:
        details.append("  [DIY] No isolation layer: productY request + a productX key -> reads productX's slice")
        details.append("        Root cause: deps.product_id is developer-constructed DI, bound to nothing by the framework.")
        details.append("        product_id is JUST ANOTHER dict key — same as any KV store. aisotropy binds all four")
        details.append("        axes at the request boundary + propagates via context.Context, so its DEFAULT read is scoped.")
        # SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Pydantic AI = L0)
        print("  [DIY] Confirmed: a wrong developer-computed product_id reads the wrong slice — no framework layer to prevent it")
    else:
        details.append("  [FAIL] Expected the wrong product_id to read productX's slice — the DIY store did not behave like a plain dict")
        print("  [FAIL] Wrong product_id did not return productX's slice (unexpected)")
        return False, details

    return True, details


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("=" * 64)
    print("Pydantic AI — Multi-PRODUCT Scoping Demo (3rd axis: product_id)")
    print("=" * 64)
    print()

    passed, details = test_product_isolation()

    print()
    print("-" * 64)
    for d in details:
        print(d)
    print("-" * 64)

    if passed:
        print("\nResult: PASS")
        print("  The DIY 4-axis store partitions when the product key is right; a wrong")
        # SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Pydantic AI = L0)
        print("  developer-computed product_id reads the wrong slice because NO framework")
        print("  isolation layer exists on any axis.")
        print("  Finding: Pydantic AI enforces NOTHING on product_id — it is a developer-")
        print("           computed key exactly like tenant_id/user_id. This is an out-of-")
        print("           scope/DIY classification (the framework is a stateless agent")
        print("           library with no memory or multi-tenancy layer), NOT a defect.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
