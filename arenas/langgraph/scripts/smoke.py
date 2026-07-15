#!/usr/bin/env python3
"""
Adversarial isolation smoke test for LangGraph.

LangGraph's `BaseStore` scopes data by a developer-chosen namespace TUPLE.
`store.get(namespace, key)` is an exact-key lookup (safe when the namespace is
right). `store.search(namespace_prefix)` is a PREFIX scan over the namespace
tuple, ELEMENT-WISE: a namespace matches when the search tuple is a prefix of it.

The leak is a SHORT PREFIX over a shared parent element. If you namespace as
`(org, tenant, user)` and then search with just `(org,)` — one element short of
full scoping — you scan across EVERY tenant nested under that org. The framework
never checks that the prefix matches the authenticated tenant; the developer owns
the depth of the prefix.

(Note: the match is element-wise, NOT string-prefix. `search(("tenant",))` does
NOT match `("tenantA", ...)` — "tenant" != "tenantA". The real, reproducible leak
is the shallow-prefix scan over a shared namespace root, demonstrated below.)

This test proves the finding DETERMINISTICALLY at the store layer — no LLM.

Matrix (mirrors aisotropy's isolation_e2e_test.go):
  1. Seed tenantA (namespaced under a shared org root) with a unique marker.
  2. POSITIVE CONTROL: exact get() of tenantA's namespace -> marker MUST appear.
  3. CORRECT SCOPING: exact search of tenantB's FULL namespace -> no marker.
  4. PREFIX LEAK: search the SHORT org-only prefix -> tenantA's marker surfaces
     alongside tenantB's data. The expected finding.

A successful run = positive control verified AND the prefix-scan leak reproduced.
"""

import random
import sys

try:
    from langgraph.store.memory import InMemoryStore
except ImportError:
    print("ERROR: langgraph not installed. Run: pip install langgraph", file=sys.stderr)
    sys.exit(1)


# ── Helpers ────────────────────────────────────────────────────────────────

def random_marker(prefix: str) -> str:
    """Generate a distinctive alphanumeric token (mirrors aisotropy's marker())."""
    return f"{prefix}{random.randbytes(10).hex()}".lower()


# ── Adversarial test ──────────────────────────────────────────────────────

def test_isolation():
    """Returns (passed: bool, details: list[str]).

    passed == True means: positive control verified AND the expected
    short-prefix cross-tenant scan leak was reproduced.
    """
    details = []

    marker = random_marker("lga")
    print(f"[langgraph] Seeding tenantA with marker: {marker}")

    store = InMemoryStore()

    # Namespaces share an org root: (org, tenant, user). This is a common,
    # reasonable-looking scoping scheme — and exactly where the leak lives.
    ns_a = ("acme", "tenantA", "userA")   # secret marker
    ns_b = ("acme", "tenantB", "userB")   # a different tenant under the same org

    store.put(ns_a, "msg-001", {"role": "user", "content": f"Store this: {marker}"})
    store.put(ns_b, "msg-002", {"role": "user", "content": "tenantB message"})

    # ── 1. POSITIVE CONTROL: exact get() of tenantA's namespace ──
    # Installed API: get(namespace: tuple, key: str) -> Item | None; item.value (not .val)
    item = store.get(ns_a, "msg-001")
    pos_ok = item is not None and marker in item.value.get("content", "")
    if pos_ok:
        details.append("  [PASS] Positive control: exact get(ns_a, 'msg-001').value read tenantA's marker")
        print("  [PASS] Positive control: tenantA read its own marker (exact get)")
    else:
        details.append("  [FAIL] Positive control: exact get() did NOT return tenantA's marker — aborting")
        print("  [FAIL] Positive control FAILED — aborting")
        return False, details

    # ── 2. CORRECT SCOPING: exact-depth search of tenantB's FULL namespace ──
    # Searching the full (org, tenant, user) prefix stays inside one tenant.
    b_full = list(store.search(ns_b))
    b_leak = any(marker in i.value.get("content", "") for i in b_full)
    if not b_leak:
        details.append("  [PASS] Correct scoping: search(('acme','tenantB','userB')) saw NO marker (full-depth prefix stays in-tenant)")
        print("  [PASS] Correct scoping: full-namespace search stayed in-tenant")
    else:
        details.append("  [FAIL] Unexpected: full-depth tenantB search saw tenantA's marker")
        print("  [FAIL] Unexpected leak with full-depth search")
        return False, details

    # ── 3. PREFIX LEAK: short org-only prefix scans across tenants ──
    print()
    print("[langgraph] Demonstrating the short-prefix scan leak:")
    print("  Namespaces are (org, tenant, user). Search with just the org element")
    print("  -> element-wise prefix match returns EVERY tenant nested under it.")
    print("  Searching store.search(('acme',))  # one element short of full scoping")
    results = list(store.search(("acme",)))
    scanned = [(i.namespace, i.value.get("content", "")) for i in results]
    print(f"  -> {len(results)} items across namespaces: {[ns for ns, _ in scanned]}")
    prefix_leak = any(marker in content for _, content in scanned)
    if prefix_leak:
        details.append("  [LEAK] Prefix leak: search(('acme',)) scanned across tenants -> tenantA's marker surfaced")
        details.append("         Root cause: search() is an element-wise prefix scan. A prefix shorter than the")
        details.append("         full namespace crosses tenant boundaries. The developer owns the prefix depth;")
        details.append("         the framework never checks it against the authenticated tenant.")
        print("  [LEAK] Prefix leak confirmed: short prefix -> cross-tenant scan")
    else:
        details.append("  [FAIL] Expected prefix leak did NOT reproduce (marker absent from short-prefix scan)")
        print("  [FAIL] Expected prefix leak did not reproduce")
        return False, details

    print()
    print("[langgraph] Isolation model summary:")
    print("  store.get(namespace, key)   -> exact-key lookup (safe if namespace is correct)")
    print("  store.search(prefix)        -> ELEMENT-WISE prefix scan (leaks if prefix is too shallow)")
    print("  No framework check that the prefix matches the authenticated tenant.")
    print("  vs. aisotropy: exact-key scoping + Scope{} value objects = unrepresentable leaks")

    return True, details


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print("=" * 60)
    print("LangGraph — Adversarial Isolation Smoke Test")
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
        print("  Positive control verified AND the expected short-prefix scan leak reproduced.")
        print("  Finding: store.search() is an element-wise prefix scan. A namespace prefix")
        print("           shallower than full scoping crosses tenant boundaries. Namespace")
        print("           depth is developer-owned convention, not framework-enforced.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
