#!/usr/bin/env python3
"""
Isolation smoke test for LangGraph.

What this IS and IS NOT
-----------------------
This exercises the RAW `InMemoryStore` primitive with NO authorization layer.
`BaseStore` scopes data by a developer-chosen namespace TUPLE.
`store.get(namespace, key)` is an exact-key lookup (safe when the namespace is
right). `store.search(namespace_prefix)` is a PREFIX scan over the namespace
tuple, ELEMENT-WISE: a namespace matches when the search tuple is a prefix of it.

The naive-config bug is a SHORT PREFIX over a shared parent element. Namespace as
`(org, tenant, user)` and then search with just `(org,)` — one element short of
full scoping — and you scan across EVERY tenant nested under that org. On the raw
primitive, prefix depth is a DEVELOPER CONVENTION; the bare store does not check it
against the authenticated tenant. That is not a framework defect — it is the same
under-scoped-query property any KV store has when hit with no scoping layer above.

The honest full picture (both configs, below):
  * PART A — raw store + naive convention: a shallow prefix crosses tenants
    (a developer bug the primitive does not catch).
  * PART B — correct config: full-depth namespaces + an identity-bound layer
    (the `@auth.on.store()` analogue, importable from `langgraph_sdk`, runs on
    EVERY store op) make the cross-tenant scan structurally impossible. A
    competent dev CAN close this. Part B demonstrates the equivalent as plain
    app code on top of the OSS store.

(Note: the match is element-wise, NOT string-prefix. `search(("tenant",))` does
NOT match `("tenantA", ...)` — "tenant" != "tenantA". The reproduced bug is the
shallow-prefix scan over a shared namespace root, demonstrated below.)

This test proves both DETERMINISTICALLY at the store layer — no LLM.

Matrix (mirrors aisotropy's isolation_e2e_test.go):
  PART A (raw store, naive convention):
    1. Seed tenantA (namespaced under a shared org root) with a unique marker.
    2. POSITIVE CONTROL: exact get() of tenantA's namespace -> marker MUST appear.
    3. NAIVE-CONFIG CONTROL: exact search of tenantB's FULL namespace -> no marker.
    4. SHORT-PREFIX BUG: search the SHORT org-only prefix -> tenantA's marker
       surfaces alongside tenantB's data.
  PART B (correct config):
    5. IDENTITY-BOUND: the same short-prefix search rewritten to the caller's own
       namespace -> tenantA's marker never returned. Cross-tenant scan closed.

A successful run = Part A reproduces the naive-config bug AND Part B closes it.
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


class TenantScopedStore:
    """Identity-bound facade over a raw BaseStore — the plain-app-code analogue
    of a LangGraph server ``@auth.on.store()`` handler.

    It forces ``namespace[:3] == (org, tenant, user)`` of the AUTHENTICATED caller
    on every op: get/put/delete assert the prefix (rejecting a mismatch), and
    search is rewritten to the caller's own namespace so a shallow prefix can never
    widen the scan. Runs on the OSS ``InMemoryStore`` with no server; a real
    ``@auth.on.store()`` handler does the equivalent inside a LangGraph server.
    """

    def __init__(self, store, org: str, tenant: str, user: str):
        self._store = store
        self._identity = (org, tenant, user)

    def _assert_owned(self, namespace):
        if tuple(namespace[:3]) != self._identity:
            raise PermissionError(
                f"namespace {namespace} not owned by authenticated caller {self._identity}"
            )
        return namespace

    def put(self, namespace, key, value):
        return self._store.put(self._assert_owned(namespace), key, value)

    def get(self, namespace, key):
        return self._store.get(self._assert_owned(namespace), key)

    def search(self, namespace_prefix, **kwargs):
        # Caller-supplied prefix ignored for scope; identity wins.
        return self._store.search(self._identity, **kwargs)


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

    # ── 2. NAIVE-CONFIG CONTROL: exact-depth search of tenantB's FULL namespace ──
    # Searching the full (org, tenant, user) prefix stays inside one tenant.
    b_full = list(store.search(ns_b))
    b_leak = any(marker in i.value.get("content", "") for i in b_full)
    if not b_leak:
        details.append("  [PASS] Naive-config control: search(('acme','tenantB','userB')) saw NO marker (full-depth prefix stays in-tenant)")
        print("  [PASS] Naive-config control: full-namespace search stayed in-tenant")
    else:
        details.append("  [FAIL] Unexpected: full-depth tenantB search saw tenantA's marker")
        print("  [FAIL] Unexpected leak with full-depth search")
        return False, details

    # ── 3. SHORT-PREFIX BUG: short org-only prefix scans across tenants ──
    print()
    print("[langgraph] PART A (raw store, naive convention) — short-prefix scan bug:")
    print("  Namespaces are (org, tenant, user). Search with just the org element")
    print("  -> element-wise prefix match returns EVERY tenant nested under it.")
    print("  Searching store.search(('acme',))  # one element short of full scoping")
    results = list(store.search(("acme",)))
    scanned = [(i.namespace, i.value.get("content", "")) for i in results]
    print(f"  -> {len(results)} items across namespaces: {[ns for ns, _ in scanned]}")
    prefix_leak = any(marker in content for _, content in scanned)
    if prefix_leak:
        details.append("  [BUG] Short-prefix: search(('acme',)) scanned across tenants -> tenantA's marker surfaced")
        details.append("        Cause: the DEVELOPER under-scoped the query. search() is an element-wise prefix")
        details.append("        scan; the raw primitive has no auth layer to check the prefix against the caller.")
        details.append("        This is a KV-store property, not a framework defect — Part B closes it.")
        print("  [BUG] Short-prefix bug reproduced (naive config): short prefix -> cross-tenant scan")
    else:
        details.append("  [FAIL] Expected short-prefix bug did NOT reproduce (marker absent from short-prefix scan)")
        print("  [FAIL] Expected short-prefix bug did not reproduce")
        return False, details

    # ── 4. IDENTITY-BOUND CONFIG: the @auth.on.store() analogue closes the bug ──
    print()
    print("[langgraph] PART B (correct config) — identity-bound authorization layer:")
    print("  TenantScopedStore forces namespace[:3] == the caller's (org,tenant,user)")
    print("  on every op — the plain-app-code equivalent of a LangGraph server")
    print("  @auth.on.store() handler (importable from langgraph_sdk).")
    a_store = TenantScopedStore(store, "acme", "tenantA", "userA")
    b_results = list(a_store.search(("acme",)))
    print(f"  Replay: a tenantA-bound store.search(('acme',)) -> namespaces {[i.namespace for i in b_results]}")
    cross_tenant = any(i.namespace == ns_b for i in b_results)
    if not cross_tenant:
        details.append("  [FIXED] Identity-bound search was rewritten to (acme,tenantA,userA); tenantB's")
        details.append("          namespace was NOT returned. The short-prefix cross-tenant scan is eliminated.")
        print("  [FIXED] Short-prefix cross-tenant scan eliminated by identity binding")
    else:
        details.append("  [FAIL] Identity-bound store still returned tenantB's namespace")
        print("  [FAIL] Part B did not eliminate the cross-tenant scan")
        return False, details

    print()
    print("[langgraph] Isolation model summary:")
    print("  RAW store (Part A): store.search(prefix) is an element-wise prefix scan; a shallow")
    print("    prefix crosses tenants. Prefix depth is developer convention — the bare primitive")
    print("    checks nothing. Same under-scoped-query property as any KV store with no auth layer.")
    print("  CORRECT config (Part B): an @auth.on.store()-style identity-bound layer binds")
    print("    namespace[:3] to the verified caller on every op — the shallow-prefix scan vanishes.")
    print("  vs. aisotropy: the same binding, but wired BY DEFAULT as an unforgeable Scope value")
    print("    object carried via context.Context. LangGraph gives you the tools; you must wire")
    print("    them. aisotropy's own raw stores likewise rely on its Scope/handler layer — isolation")
    print("    is an application-boundary property in both; aisotropy's edge is default-wired.")

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
        print("  Part A: the naive-config short-prefix bug reproduced on the raw store.")
        print("  Part B: an identity-bound (@auth.on.store()-style) layer eliminated it.")
        print("  Finding: on the RAW store, namespace depth is developer convention — a shallow")
        print("           prefix crosses tenants (a KV-store property, not a framework defect).")
        print("           A competent dev CAN close this with full-depth namespaces + the auth")
        print("           handler. aisotropy's edge is that the binding is wired BY DEFAULT.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
