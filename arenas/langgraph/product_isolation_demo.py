#!/usr/bin/env python3
"""
PRODUCT-axis isolation demo for LangGraph (3rd isolation axis).

What this demo IS and IS NOT
----------------------------
This exercises the RAW `InMemoryStore` primitive with NO authorization layer —
the default/naive posture. In that posture, a namespace is just a developer-chosen
tuple: `BaseStore` has no `product` concept, and namespace depth is a DEVELOPER
CONVENTION, not something the primitive enforces. That is not a framework defect —
it is true of any key/value store (aisotropy's own BadgerDB/Qdrant included) when
you hit the bare primitive with no scoping layer above it.

The honest thesis is narrower than "the framework leaks":
  * With the raw store + naive convention, a shallow prefix or a wrong product
    string reads another product's data. Both are DEVELOPER BUGS the primitive
    does not catch (Part A below).
  * With full-depth namespaces + an identity-bound authorization layer, BOTH
    leaks become structurally impossible (Part B below). LangGraph ships that
    layer as the server-side `@auth.on.store()` handler (importable from
    `langgraph_sdk`, runs on EVERY store op: put/get/search/delete). It can
    rewrite/assert the namespace against the authenticated caller so a competent
    dev CAN make cross-product reads impossible. Part B demonstrates the
    equivalent as plain app code any dev can add on top of the OSS store.

Where aisotropy actually differs:
  aisotropy binds 3 axes (tenant_id, user_id, session_id) from
  the verified identity via context.Context BY DEFAULT, as an unforgeable Scope
  value object with unexported fields. LangGraph gives you the tools (deep
  namespaces + the auth handler) but you must WIRE them. aisotropy's own raw
  stores also rely on its Scope/handler layer — isolation is an application-
  boundary property in BOTH. aisotropy's edge is that the binding is wired by
  default and the value object cannot carry a wrong/forgotten key.

Residual honest caveat: the `@auth.on.store()` namespace-REWRITE runs inside a
LangGraph server (self-hosted or the managed Platform tier — relevant to
sovereignty). The namespace-ASSERTION pattern shown in Part B is plain app code
that runs on the OSS store with no server at all.

What this proves DETERMINISTICALLY (no LLM, fully offline)
----------------------------------------------------------
Scope model: namespace = (tenant, user, product).

  PART A — raw store + naive convention (developer bugs the primitive won't catch):
    1. POSITIVE CONTROL — exact get() of productX's own namespace reads its
       own marker. Correct scoping works.
    2a. SHALLOW-PREFIX BUG — a handler that FORGETS the product element,
        search(("acme","userA")), scans every product under that user.
    2b. WRONG-PRODUCT BUG — an exact get() built with the wrong product string
        returns the other product's data. The raw store validates nothing.
    3. NAIVE-CONFIG CONTROL — exact get() at the CORRECT full depth stays in
       productY. Full-depth scoping is safe IF the developer supplies the right
       element every time.

  PART B — correct config (identity-bound scoping = the @auth.on.store() analogue):
    4. Both bugs from Part A are replayed against an identity-bound store facade
       that forces namespace[:3] == the caller's (org,user,product) on every op.
       The shallow-prefix search is rewritten to the caller's own namespace
       (productX never returned), and the wrong-product get() is REJECTED.
       Cross-product read is structurally impossible.

A successful run = Part A reproduces both naive-config bugs AND Part B proves
the identity-bound config closes both.
"""

import random
import sys

try:
    from langgraph.store.memory import InMemoryStore
except ImportError:
    print("ERROR: langgraph not installed. Run: pip install langgraph", file=sys.stderr)
    sys.exit(1)


def random_marker(prefix: str) -> str:
    return f"{prefix}{random.randbytes(10).hex()}".lower()


class ProductScopedStore:
    """Identity-bound facade over a raw BaseStore — the plain-app-code analogue
    of a LangGraph server ``@auth.on.store()`` handler.

    It forces ``namespace[:3] == (org, user, product)`` of the AUTHENTICATED
    caller on every op: put/get/delete assert the prefix (rejecting a mismatch),
    and search is rewritten to the caller's own namespace so a shallow prefix can
    never widen the scan. This runs on the OSS ``InMemoryStore`` with no server;
    a real ``@auth.on.store()`` handler does the equivalent inside a LangGraph
    server (self-hosted or the managed Platform tier).
    """

    def __init__(self, store, org: str, user: str, product: str):
        self._store = store
        self._identity = (org, user, product)

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

    def delete(self, namespace, key):
        return self._store.delete(self._assert_owned(namespace), key)

    def search(self, namespace_prefix, **kwargs):
        # The caller-supplied prefix is IGNORED for scope; identity wins. A
        # shallow prefix cannot widen the scan beyond the caller's namespace.
        return self._store.search(self._identity, **kwargs)


def test_product_isolation():
    """Returns (passed: bool, details: list[str])."""
    details = []

    marker_x = random_marker("prodX-secret-")
    print(f"[langgraph] Seeding tenantA/userA/productX with private marker: {marker_x}")

    store = InMemoryStore()

    # All three namespaces share tenant+user; they differ ONLY by the product
    # element — the 3rd axis. This is exactly how a multi-product deployment
    # would namespace per-product memory in LangGraph.
    ns_x = ("acme", "userA", "productX")   # holds the private marker
    ns_y = ("acme", "userA", "productY")   # a DIFFERENT product, same user

    store.put(ns_x, "note-1", {"content": f"productX private data: {marker_x}"})
    store.put(ns_y, "note-1", {"content": "productY has its own unrelated note"})

    # ── 1. POSITIVE CONTROL ────────────────────────────────────────────────
    item = store.get(ns_x, "note-1")
    pos_ok = item is not None and marker_x in item.value.get("content", "")
    if pos_ok:
        details.append("  [PASS] Positive control: exact get(productX ns) read productX's own marker")
        print("  [PASS] Positive control: productX read its own marker (exact get)")
    else:
        details.append("  [FAIL] Positive control: exact get did NOT return productX's marker — aborting")
        print("  [FAIL] Positive control FAILED — aborting")
        return False, details

    # ── PART A: raw store + naive convention (developer bugs) ───────────────
    # ── 2a. SHALLOW-PREFIX BUG across products ──────────────────────────────
    print()
    print("[langgraph] PART A (raw store, naive convention)")
    print("[langgraph] Bug 2a — a handler 'serving productY' under-scopes to (tenant,user):")
    print("  search(('acme','userA'))  # product element FORGOTTEN — one short of full scope")
    results = list(store.search(("acme", "userA")))
    scanned = [(i.namespace, i.value.get("content", "")) for i in results]
    print(f"  -> {len(results)} items across namespaces: {[ns for ns, _ in scanned]}")
    prefix_leak = any(marker_x in content for _, content in scanned)
    if prefix_leak:
        details.append("  [BUG 2a] Shallow-prefix: search(('acme','userA')) crossed the product axis")
        details.append("           -> productX's private marker surfaced to a productY-context handler.")
        details.append("           Cause: the DEVELOPER under-scoped. The raw primitive has no product")
        details.append("           concept and no auth layer, so it does not force the element or bind it")
        details.append("           to the caller. True of any bare KV store — see Part B for the fix.")
        print("  [BUG 2a] Cross-product prefix bug reproduced (naive config)")
    else:
        details.append("  [FAIL] Expected shallow-prefix cross-product bug did NOT reproduce")
        print("  [FAIL] Expected prefix bug did not reproduce")
        return False, details

    # ── 2b. WRONG-PRODUCT SUBSTITUTION (exact get, wrong element) ───────────
    print()
    print("[langgraph] Bug 2b — request is for productY, but code passes the WRONG product element:")
    print("  productY handler builds namespace with productX (a stale/wrong variable) and reads it.")
    # The RAW store performs no authorization — it returns whatever namespace it
    # is handed. This is a same-key/wrong-key property of any KV store with no
    # scoping layer, not a LangGraph-specific defect.
    wrong_ns = ("acme", "userA", "productX")   # productY handler *should* use productY
    leaked = store.get(wrong_ns, "note-1")
    subst_leak = leaked is not None and marker_x in leaked.value.get("content", "")
    if subst_leak:
        details.append("  [BUG 2b] Wrong-product substitution: an exact get() with a mismatched product")
        details.append("           element returned productX's private data. The RAW store performs no")
        details.append("           authorization — one wrong variable = cross-product read. Part B closes it.")
        print("  [BUG 2b] Wrong-product substitution bug reproduced (naive config)")
    else:
        details.append("  [FAIL] Wrong-product substitution did not return productX's data")
        print("  [FAIL] Substitution bug did not reproduce")
        return False, details

    # ── 3. NAIVE-CONFIG CONTROL: correct full-depth scoping IS safe ─────────
    print()
    print("[langgraph] Naive-config control — CORRECT full-depth exact get for productY:")
    safe = store.get(ns_y, "note-1")
    safe_ok = safe is not None and marker_x not in safe.value.get("content", "")
    if safe_ok:
        details.append("  [PASS] Naive-config control: exact get at the CORRECT (tenant,user,productY) depth")
        details.append("         did NOT see productX's marker. Full-depth scoping is safe — IF the developer")
        details.append("         supplies the right element every time. Part B removes that 'if'.")
        print("  [PASS] Correct full-depth scoping stayed inside productY")
    else:
        details.append("  [FAIL] Naive-config control unexpected result")
        print("  [FAIL] Naive-config control unexpected")
        return False, details

    # ── PART B: correct config — identity-bound scoping (@auth.on.store analogue) ──
    print()
    print("[langgraph] PART B (correct config — identity-bound authorization layer)")
    print("  ProductScopedStore forces namespace[:3] == the caller's (org,user,product)")
    print("  on EVERY op — the plain-app-code equivalent of a LangGraph server")
    print("  @auth.on.store() handler (importable from langgraph_sdk).")
    y_store = ProductScopedStore(store, "acme", "userA", "productY")

    # Replay 2a: the shallow prefix is rewritten to the caller's own namespace.
    b_results = list(y_store.search(("acme", "userA")))
    b_prefix_leak = any(marker_x in i.value.get("content", "") for i in b_results)
    print(f"  Replay 2a: search(('acme','userA')) -> namespaces {[i.namespace for i in b_results]}")
    if not b_prefix_leak:
        details.append("  [FIXED 2a] Identity-bound search was rewritten to (acme,userA,productY);")
        details.append("             productX's marker was NOT returned. Shallow-prefix leak eliminated.")
        print("  [FIXED 2a] Shallow-prefix leak eliminated by identity binding")
    else:
        details.append("  [FAIL] Identity-bound store still leaked on the shallow-prefix replay")
        print("  [FAIL] Part B did not eliminate the prefix leak")
        return False, details

    # Replay 2b: the wrong-product exact get() is rejected outright.
    try:
        y_store.get(("acme", "userA", "productX"), "note-1")
        rejected = False
    except PermissionError:
        rejected = True
    if rejected:
        details.append("  [FIXED 2b] Wrong-product get() was REJECTED (namespace not owned by caller).")
        details.append("             Cross-product read is structurally impossible under identity binding.")
        print("  [FIXED 2b] Wrong-product substitution rejected by identity binding")
    else:
        details.append("  [FAIL] Identity-bound store did not reject the wrong-product get()")
        print("  [FAIL] Part B did not reject the wrong-product read")
        return False, details

    print()
    print("[langgraph] Product-axis isolation summary:")
    print("  RAW store (Part A): namespace depth is developer convention; the bare")
    print("    primitive catches neither a forgotten element (2a) nor a wrong one (2b).")
    print("    This is a same-key/wrong-key property of any KV store, not a defect.")
    print("  CORRECT config (Part B): an @auth.on.store()-style identity-bound layer")
    print("    binds namespace[:3] to the verified caller on every op — both leaks vanish.")
    print("  vs. aisotropy: the same binding, but wired BY DEFAULT as an unforgeable Scope")
    print("    value object (unexported fields) carried via context.Context. LangGraph gives")
    print("    you the tools; you must wire them. aisotropy's own raw stores likewise rely on")
    print("    its Scope/handler layer — isolation is an application-boundary property in both;")
    print("    aisotropy's edge is default-wired + a key you cannot forge or forget.")

    return True, details


if __name__ == "__main__":
    print("=" * 68)
    print("LangGraph — Adversarial PRODUCT Isolation Demo (3rd axis)")
    print("=" * 68)
    print()

    passed, details = test_product_isolation()

    print()
    print("-" * 68)
    for d in details:
        print(d)
    print("-" * 68)

    if passed:
        print("\nResult: PASS")
        print("  Part A: both naive-config bugs reproduced on the raw store.")
        print("  Part B: an identity-bound (@auth.on.store()-style) layer eliminated both.")
        print("  Finding: on the RAW store, product_id is developer convention — a shallow")
        print("           prefix or wrong variable reads another product (a KV-store property,")
        print("           not a framework defect). A competent dev CAN make cross-product reads")
        print("           impossible with full-depth namespaces + the auth handler. aisotropy's")
        print("           edge is that this binding is wired BY DEFAULT as an unforgeable Scope.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
