#!/usr/bin/env python3
"""
Adversarial PRODUCT isolation demo for LangGraph (3rd isolation axis).

Background
----------
aisotropy scopes every memory read/write by a Scope VALUE OBJECT carrying
FOUR axes: (tenant_id, user_id, session_id, product_id). All four are bound to
the authenticated identity at the Huma handler boundary and travel via
context.Context — a wrong product_id cannot be represented because the Scope is
constructed from the verified caller, not from developer-supplied strings.

The earlier LangGraph smoke test proved the TENANT/USER axis leaks via a shallow
namespace prefix. This demo isolates the THIRD axis — product_id — and shows the
same structural weakness: in LangGraph, product_id is just one more element the
DEVELOPER stuffs into the namespace tuple. `BaseStore` has no `product` concept,
no binding of that element to the caller, and no enforcement that a request
serving productY only ever reads productY's namespace. Namespace depth is
convention; the framework checks nothing.

What this proves DETERMINISTICALLY (no LLM, fully offline)
----------------------------------------------------------
Scope model: namespace = (tenant, user, product).

  1. POSITIVE CONTROL — tenantA/userA/productX does an exact get() of its own
     namespace and reads its own private marker. (Correct scoping works.)

  2. CROSS-PRODUCT LEAK — the SAME tenant+user, but a handler operating in a
     DIFFERENT product context, reads productX's marker. Two independent leak
     mechanisms are shown, both because product_id is unbound convention:

       2a. SHALLOW-PREFIX LEAK: a handler that scopes to (tenant, user) but
           FORGETS the product element — search(("acme","userA")) — scans
           EVERY product under that user and surfaces productX's marker while
           "serving" productY. The framework never forces the product element
           into the query.

       2b. WRONG-PRODUCT SUBSTITUTION: the framework will happily read whatever
           product string the developer hands it. There is no check that the
           product element matches the authenticated product. A single wrong
           variable (productX passed where productY belongs) crosses the
           boundary with a plain exact get() — the store returns the data.

  3. HONESTY CONTROL — an exact get() at the CORRECT full (tenant,user,productY)
     depth returns None for productX's key. Exact scoping IS safe *if the
     developer gets the product element right every single time*. That "if" is
     exactly what aisotropy removes: the product axis is framework-bound, not
     developer-remembered.

A successful run = positive control verified AND both cross-product leak
mechanisms reproduced.
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

    # ── 2a. SHALLOW-PREFIX LEAK across products ─────────────────────────────
    print()
    print("[langgraph] Leak 2a — a handler 'serving productY' under-scopes to (tenant,user):")
    print("  search(('acme','userA'))  # product element FORGOTTEN — one short of full scope")
    results = list(store.search(("acme", "userA")))
    scanned = [(i.namespace, i.value.get("content", "")) for i in results]
    print(f"  -> {len(results)} items across namespaces: {[ns for ns, _ in scanned]}")
    prefix_leak = any(marker_x in content for _, content in scanned)
    if prefix_leak:
        details.append("  [LEAK 2a] Shallow-prefix: search(('acme','userA')) crossed the product axis")
        details.append("            -> productX's private marker surfaced to a productY-context handler.")
        details.append("            Root cause: the product element is developer-owned namespace depth; the")
        details.append("            framework never forces it into the query nor binds it to the caller.")
        print("  [LEAK 2a] Cross-product prefix leak confirmed")
    else:
        details.append("  [FAIL] Expected shallow-prefix cross-product leak did NOT reproduce")
        print("  [FAIL] Expected prefix leak did not reproduce")
        return False, details

    # ── 2b. WRONG-PRODUCT SUBSTITUTION (exact get, wrong element) ───────────
    print()
    print("[langgraph] Leak 2b — request is for productY, but code passes the WRONG product element:")
    print("  productY handler builds namespace with productX (a stale/wrong variable) and reads it.")
    # There is NO framework check that this product element matches the caller's
    # authenticated product. The store returns productX's data verbatim.
    wrong_ns = ("acme", "userA", "productX")   # productY handler *should* use productY
    leaked = store.get(wrong_ns, "note-1")
    subst_leak = leaked is not None and marker_x in leaked.value.get("content", "")
    if subst_leak:
        details.append("  [LEAK 2b] Wrong-product substitution: an exact get() with a mismatched product")
        details.append("            element returned productX's private data. The framework never validates")
        details.append("            the product element against the authenticated identity — product_id is")
        details.append("            just a string the developer supplies. One wrong variable = cross-product read.")
        print("  [LEAK 2b] Wrong-product substitution leak confirmed")
    else:
        details.append("  [FAIL] Wrong-product substitution did not return productX's data")
        print("  [FAIL] Substitution leak did not reproduce")
        return False, details

    # ── 3. HONESTY CONTROL: correct full-depth scoping IS safe ──────────────
    print()
    print("[langgraph] Honesty control — CORRECT full-depth exact get for productY:")
    safe = store.get(ns_y, "note-1")
    safe_ok = safe is not None and marker_x not in safe.value.get("content", "")
    if safe_ok:
        details.append("  [PASS] Honesty control: exact get at the CORRECT (tenant,user,productY) depth did")
        details.append("         NOT see productX's marker. Exact scoping is safe — IF the developer supplies")
        details.append("         the right product element every single time. That 'if' is the whole risk.")
        print("  [PASS] Correct full-depth scoping stayed inside productY")
    else:
        details.append("  [FAIL] Honesty control unexpected result")
        print("  [FAIL] Honesty control unexpected")
        return False, details

    print()
    print("[langgraph] Product-axis isolation summary:")
    print("  BaseStore has no 'product' concept — product_id is one more element in the")
    print("  namespace tuple, chosen and typed by the developer on every call.")
    print("  Nothing binds that element to the authenticated caller; nothing forces it")
    print("  into a search. A forgotten element (2a) or a wrong variable (2b) crosses the")
    print("  product boundary silently.")
    print("  vs. aisotropy: product_id is a field of the Scope value object, constructed")
    print("  from the verified identity at the handler boundary and carried via context.Context.")
    print("  A wrong/forgotten product_id is unrepresentable — not a convention to remember.")

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
        print("  Positive control verified AND both cross-product leak mechanisms reproduced.")
        print("  Finding: product_id is unbound developer convention in LangGraph. The store")
        print("           never binds the product element to the caller nor forces it into a")
        print("           query — a shallow prefix or a wrong variable reads another product's")
        print("           private data with no framework objection.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — the demonstration did not run as expected (see above).")
        sys.exit(1)
