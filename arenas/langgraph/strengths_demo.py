#!/usr/bin/env python3
"""
Steelman demo — what LangGraph is GENUINELY great at (RUN-VERIFIED).

This is the two-sided counterpart to the isolation demos. Those show that
multi-tenant isolation is wiring you own in LangGraph (vs. wired-by-default in
aisotropy). This file demonstrates the flip side, honestly: LangGraph ships a
DURABLE STATEFUL GRAPH runtime that aisotropy simply does not have. aisotropy is
a request/response LLM-OS with a memory hierarchy; it has NO durable graph, NO
checkpoint/resume, NO human-in-the-loop pause topology, NO time-travel. On these
axes LangGraph wins outright — and saying so is what makes the whole arena
credible.

Every strength below is exercised against the PINNED installed version
(langgraph 1.2.9) using its real public API — module + symbol cited inline — and
the demo PRINTS the observed result. Nothing here is asserted without running it.

Strengths demonstrated
----------------------
  S1. Durable execution + human-in-the-loop (interrupt / resume)
      API: langgraph.graph.StateGraph, langgraph.checkpoint.memory.InMemorySaver,
           langgraph.types.interrupt, langgraph.types.Command
      A graph pauses mid-run at interrupt(); its state lives in the checkpointer,
      not the graph object. A BRAND-NEW compiled graph bound to the SAME saver
      resumes exactly where it stopped via Command(resume=...). This is durable
      execution + a first-class HITL approval gate. aisotropy has no equivalent.

  S2. Time-travel over checkpoint history
      API: CompiledGraph.get_state_history(config)
      Every super-step is a persisted checkpoint. You can enumerate the full
      history and fork execution from any prior checkpoint. aisotropy has no
      checkpoint timeline to travel.

  S3. First-class streaming modes on a cyclic graph
      API: CompiledGraph.stream(input, stream_mode="values"|"updates"),
           langgraph.graph.add_conditional_edges (cycles)
      The graph is CYCLIC (a node loops back to itself until a condition) — an
      agent-loop topology — and the same run is observable two ways: full state
      snapshots ("values") vs. per-node deltas ("updates"). aisotropy streams
      LLM token deltas over SSE, but has no graph-topology stream.

A successful run = all three strengths execute and self-validate (exit 0).
"""

import sys
from typing import TypedDict

try:
    from langgraph.graph import StateGraph, START, END
    from langgraph.checkpoint.memory import InMemorySaver
    from langgraph.types import interrupt, Command
except ImportError:
    print("ERROR: langgraph not installed. Run: pip install langgraph", file=sys.stderr)
    sys.exit(1)


def rule(title: str) -> None:
    print("\n" + "=" * 68)
    print(title)
    print("=" * 68)


# ── S1. Durable execution + human-in-the-loop (interrupt / resume) ──────────
def strength_durable_hitl():
    """Returns (passed, note)."""
    rule("S1 — Durable execution + human-in-the-loop (interrupt / resume)")

    class State(TypedDict):
        amount: int
        decision: str

    def draft(s: State):
        # a unit of work before the human gate
        return {"amount": s["amount"] + 100}

    def approval_gate(s: State):
        # interrupt() SUSPENDS the graph and persists a checkpoint. Execution
        # does not continue until a human resumes with a value.
        decision = interrupt({"review": f"approve transfer of {s['amount']}?"})
        return {"decision": decision}

    def commit(s: State):
        return {"amount": s["amount"] + 1}  # marker that post-gate work ran

    def build():
        g = StateGraph(State)
        g.add_node("draft", draft)
        g.add_node("approval_gate", approval_gate)
        g.add_node("commit", commit)
        g.add_edge(START, "draft")
        g.add_edge("draft", "approval_gate")
        g.add_edge("approval_gate", "commit")
        g.add_edge("commit", END)
        # A shared, persistent checkpointer is what makes execution durable.
        return g.compile(checkpointer=SAVER)

    cfg = {"configurable": {"thread_id": "transfer-1"}}

    # First run PAUSES at the human gate.
    app1 = build()
    first = app1.invoke({"amount": 0, "decision": ""}, cfg)
    paused = "__interrupt__" in first
    print(f"  first invoke -> paused at gate? {paused}; state so far amount={first.get('amount')}")
    if not paused:
        return False, "graph did not pause at interrupt()"

    # Simulate the process/graph object being gone: DELETE the graph instance.
    # State survives because it lives in the checkpointer, not in app1.
    del app1

    # A BRAND-NEW compiled graph, bound to the SAME saver, sees the pending node.
    app2 = build()
    snap = app2.get_state(cfg)
    print(f"  fresh graph instance recovers pending node: {snap.next}")
    print(f"  recovered interrupt payload: {snap.interrupts[0].value if snap.interrupts else None}")
    if snap.next != ("approval_gate",):
        return False, f"fresh instance did not recover the pending gate (next={snap.next})"

    # Human approves; the SAME durable run continues to completion.
    resumed = app2.invoke(Command(resume="APPROVED"), cfg)
    print(f"  resume on fresh instance -> {resumed}")
    ok = resumed.get("decision") == "APPROVED" and resumed.get("amount") == 101
    if not ok:
        return False, f"resume did not complete correctly: {resumed}"

    print("  [PASS] Paused mid-graph, recovered on a new instance, resumed to completion.")
    return True, "durable interrupt/resume across a fresh graph instance"


# ── S2. Time-travel over checkpoint history ─────────────────────────────────
def strength_time_travel():
    rule("S2 — Time-travel over checkpoint history")

    class State(TypedDict):
        steps: int

    def work(s: State):
        return {"steps": s["steps"] + 1}

    g = StateGraph(State)
    g.add_node("work", work)
    g.add_edge(START, "work")

    # cyclic: loop 'work' until steps == 3
    def route(s: State):
        return "work" if s["steps"] < 3 else END

    g.add_conditional_edges("work", route, {"work": "work", END: END})
    app = g.compile(checkpointer=SAVER)

    cfg = {"configurable": {"thread_id": "tt-1"}}
    final = app.invoke({"steps": 0}, cfg)
    print(f"  final state: {final}")

    history = list(app.get_state_history(cfg))
    print(f"  checkpoints recorded in history: {len(history)}")
    for h in history:
        print(f"    checkpoint next={h.next} values={h.values}")
    if len(history) < 2:
        return False, f"expected multiple checkpoints, got {len(history)}"

    # Pick an EARLIER checkpoint and fork a new run from it — the essence of
    # time-travel. history is newest-first; grab a mid-run checkpoint that still
    # has a pending node AND a concrete steps value, so we resume from a real
    # past point in the loop.
    forkable = [h for h in history if h.next and "steps" in h.values]
    if not forkable:
        return False, "no resumable prior checkpoint found"
    past = forkable[-1]  # the earliest still-pending checkpoint with state
    past_steps = past.values.get("steps")
    forked = app.invoke(None, past.config)  # resume execution FROM the past
    print(f"  forked from a checkpoint at steps={past_steps} -> replayed to {forked}")
    ok = forked.get("steps") == 3
    if not ok:
        return False, f"fork-from-past did not replay to completion: {forked}"

    print("  [PASS] Enumerated the checkpoint timeline and forked from a past checkpoint.")
    return True, f"{len(history)} checkpoints; forked from steps={past_steps}"


# ── S3. Streaming modes on a cyclic graph ───────────────────────────────────
def strength_streaming_modes():
    rule("S3 — First-class streaming modes on a cyclic (agent-loop) graph")

    class State(TypedDict):
        n: int

    def tick(s: State):
        return {"n": s["n"] + 1}

    def route(s: State):
        return "tick" if s["n"] < 3 else END

    g = StateGraph(State)
    g.add_node("tick", tick)
    g.add_edge(START, "tick")
    g.add_conditional_edges("tick", route, {"tick": "tick", END: END})
    app = g.compile()  # no checkpointer needed for streaming

    print("  stream_mode='values' (full state snapshot per super-step):")
    values = list(app.stream({"n": 0}, stream_mode="values"))
    for v in values:
        print(f"    {v}")

    print("  stream_mode='updates' (per-node delta per super-step):")
    updates = list(app.stream({"n": 0}, stream_mode="updates"))
    for u in updates:
        print(f"    {u}")

    # values yields each cumulative snapshot (incl. the initial input);
    # updates yields one {node: delta} per node execution (the 3 loop turns).
    ok = (
        [v["n"] for v in values] == [0, 1, 2, 3]
        and [list(u.values())[0]["n"] for u in updates] == [1, 2, 3]
        and all(list(u.keys())[0] == "tick" for u in updates)  # cycle: same node revisited
    )
    if not ok:
        return False, f"unexpected stream output values={values} updates={updates}"

    print("  [PASS] Same cyclic run observed as full snapshots and as per-node deltas.")
    return True, "values + updates modes over a self-looping node"


# ── Main ─────────────────────────────────────────────────────────────────────
# One saver shared by the durable-execution and time-travel strengths.
SAVER = InMemorySaver()


if __name__ == "__main__":
    print("=" * 68)
    print("LangGraph — Steelman: what it does GENUINELY well (run-verified)")
    print("=" * 68)
    print("langgraph 1.2.9 · durable stateful graph runtime · exercised, not asserted")

    results = []
    for fn in (strength_durable_hitl, strength_time_travel, strength_streaming_modes):
        try:
            passed, note = fn()
        except Exception as e:  # a strength that errors is a FAIL, surfaced loudly
            passed, note = False, f"raised {type(e).__name__}: {e}"
        results.append((fn.__name__, passed, note))

    print()
    print("-" * 68)
    for name, passed, note in results:
        print(f"  [{'PASS' if passed else 'FAIL'}] {name}: {note}")
    print("-" * 68)

    all_ok = all(p for _, p, _ in results)
    if all_ok:
        print("\nResult: PASS")
        print("  LangGraph genuinely ships a durable, resumable, human-in-the-loop,")
        print("  time-travelable, cyclic stateful-graph runtime — capabilities aisotropy")
        print("  does NOT have. The narrow axis where aisotropy differs is multi-tenant")
        print("  isolation-by-default (unforgeable Scope), not graph orchestration.")
        sys.exit(0)
    else:
        print("\nResult: FAIL — a demonstrated strength did not execute as expected.")
        sys.exit(1)
