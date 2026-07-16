#!/usr/bin/env python3
"""
Multi-PRODUCT isolation demo for Pydantic AI v2 — the 3rd isolation axis.

aisotropy scopes memory by FOUR keys: (tenant_id, user_id, session_id, product_id).
The arena isolation smoke (scripts/smoke.py) only exercised tenant/user. This demo
adds `product_id` and proves the same structural truth one axis deeper:

  In Pydantic AI, `product_id` is JUST ANOTHER developer-passed field on `deps`.
  The framework never binds it to an authenticated principal, an API surface, or
  anything at all. It is a dict key you chose to put in the lookup tuple. Pass the
  wrong product_id and you silently read another product's slice — exactly like
  tenant_id, because both are the same kind of thing: convention, not enforcement.

Matrix (mirrors aisotropy's isolation_e2e_test.go, extended to the product axis):
  1. Seed tenantA/userA/PRODUCT-X with a unique marker.
  2. POSITIVE CONTROL: read with the correct (tenantA, userA, productX) key ->
     marker MUST appear.
  3. CORRECT SCOPING: same tenant+user but the CORRECT other product (productY)
     reads its own slice -> NO marker (exact-key store is safe when the key is right).
  4. CROSS-PRODUCT LEAK: a request that should be scoped to productY, but the
     developer passes productX in deps (stale/copy-pasted/cached deps, a one-line
     typo). Same tenant, same user, WRONG product -> reads productX's marker.
     The framework never checks that deps.product_id matches anything. Leak.

A successful run = positive control verified AND the cross-product leak reproduced.
This demonstrates the framework enforces NOTHING on the extra key — it is convention.
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
    """Developer-passed context — FOUR axes now, all of them convention.

    The lookup key is the 4-tuple (tenant_id, user_id, session_id, product_id).
    `product_id` is no more privileged than `tenant_id`: the framework does not
    validate ANY of them against the authenticated caller. The developer owns
    every one of these fields.
    """
    tenant_id: str
    user_id: str
    session_id: str
    product_id: str
    # Simulated per-(tenant,user,session,product) memory store (in reality Redis/DB).
    memory: dict = field(default_factory=dict)


def populate_memory(deps: Deps, tenant: str, user: str, session: str, product: str, message: str):
    """Simulate storing a message in 4-axis-scoped memory (exact-key)."""
    deps.memory.setdefault((tenant, user, session, product), []).append(message)


def get_recent_messages(deps: Deps, limit: int = 5) -> list[str]:
    """Retrieve recent messages using the DEVELOPER-PASSED 4-tuple key on `deps`.

    Lookup key is (deps.tenant_id, deps.user_id, deps.session_id, deps.product_id) —
    whatever the developer stuffed into deps. If product_id is the wrong product,
    this returns the wrong product's data. No framework guard exists on ANY axis.
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
    """Retrieves recent conversation context via the developer-passed deps key."""
    messages = get_recent_messages(ctx.deps)
    if messages:
        return "Recent messages:\n" + "\n".join(f"- {m}" for m in messages)
    return "No recent messages."


def framework_wiring_ok(shared_memory: dict) -> bool:
    """Prove the installed Pydantic AI API boots with the 4-axis deps: DI + typed output."""
    try:
        deps = Deps(tenant_id="tenantA", user_id="userA",
                    session_id="shared-session", product_id="productX",
                    memory=shared_memory)
        result = agent.run_sync("please give a status update", deps=deps)
        return isinstance(result.output, ChatResponse)  # installed API: result.output
    except Exception as exc:  # pragma: no cover - defensive
        print(f"  ! framework wiring probe raised: {exc}")
        return False


# ── Adversarial test ──────────────────────────────────────────────────────

def test_product_isolation():
    """Returns (passed: bool, details: list[str]).

    passed == True means: positive control verified AND the expected
    cross-PRODUCT convention leak was reproduced.
    """
    details = []

    marker = random_marker("pmprod")
    print(f"[pydantic-ai] Seeding tenantA/userA/PRODUCT-X with marker: {marker}")

    # Shared DIY store (dict). One store; developer-passed 4-tuple selects the slice.
    shared_memory: dict = {}

    # Seed ONLY tenantA/userA/shared-session/PRODUCT-X with the secret marker.
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

    # Framework wiring probe (non-fatal): proves the installed API constructs and runs.
    if framework_wiring_ok(shared_memory):
        details.append("  [PASS] Framework wiring: Agent(model=, output_type=) + RunContext DI runs, result.output typed (4-axis deps)")
        print("  [PASS] Framework wiring: Agent + RunContext DI runs with 4-axis deps (result.output typed)")
    else:
        details.append("  [WARN] Framework wiring probe did not return a typed output (isolation still asserted at store level)")
        print("  [WARN] Framework wiring probe inconclusive (isolation asserted at store level)")

    # ── 2. CORRECT SCOPING: same tenant+user, CORRECT other product (productY) ──
    # Everything identical EXCEPT product_id — proves the axis actually partitions
    # when the key is right, so the leak below is about the wrong key, not a broken store.
    productY_correct = Deps("tenantA", "userA", "shared-session", "productY", memory=shared_memory)
    y_correct_leak = marker in " ".join(get_recent_messages(productY_correct))
    if not y_correct_leak:
        details.append("  [PASS] Correct scoping: productY (same tenant+user) saw NO marker (product axis partitions when the key is right)")
        print("  [PASS] Correct scoping: productY with its own key saw no marker")
    else:
        details.append("  [FAIL] Unexpected: productY with its own correct key saw productX's marker (store is broken, not just convention)")
        print("  [FAIL] Unexpected leak with correct productY key")
        return False, details

    # ── 3. CROSS-PRODUCT LEAK: productY request, developer passes productX ──
    # SAME tenant, SAME user — only product_id is wrong. This is the whole point:
    # product_id is not tied to anything the caller authenticated as. It is a field
    # the developer set. Set it wrong (stale deps, copy-paste, cache) and you cross
    # the product boundary with no framework objection.
    print()
    print("[pydantic-ai] Demonstrating the CROSS-PRODUCT leak:")
    print("  Request should be scoped to productY, but deps.product_id was set to 'productX'.")
    print("  SAME tenant, SAME user — only the product key differs.")
    print("  get_recent_messages() (what the get_context tool calls) trusts deps verbatim.")
    leaky = Deps(
        tenant_id="tenantA",   # same tenant
        user_id="userA",       # same user
        session_id="shared-session",
        product_id="productX",  # <-- WRONG product passed for a productY request
        memory=shared_memory,
    )
    leak_hit = marker in " ".join(get_recent_messages(leaky))
    if leak_hit:
        details.append("  [LEAK] Cross-product leak: productY request + productX key -> read productX's marker")
        details.append("         Root cause: deps.product_id is developer-passed, not framework-enforced.")
        details.append("         product_id is JUST ANOTHER dict key — the framework binds it to nothing.")
        print("  [LEAK] Cross-product leak confirmed: wrong product_id -> cross-product read")
    else:
        details.append("  [FAIL] Expected cross-product leak did NOT reproduce")
        print("  [FAIL] Expected leak did not reproduce")
        return False, details

    return True, details


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("=" * 64)
    print("Pydantic AI — Multi-PRODUCT Isolation Demo (3rd axis: product_id)")
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
        print("  Positive control verified AND the expected cross-PRODUCT leak reproduced.")
        print("  Finding: Pydantic AI enforces NOTHING on product_id. It is a developer-passed")
        print("           key exactly like tenant_id/user_id — convention, not enforcement.")
        print("           Same tenant + same user + WRONG product_id silently reads the other")
        print("           product's data. The framework never binds product_id to a principal.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
