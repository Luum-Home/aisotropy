"""Minimal "hello agent" app on LangGraph — footprint benchmark subject.

The MODEL IS HELD CONSTANT as a deterministic offline stub (no provider, no
network): the graph node just echoes. This measures the LangGraph + CPython
runtime FOOTPRINT (image/deps/RSS/cold-start), not model behaviour.

Boot sequence, mirroring the other subjects: import the framework -> build one
trivial single-node graph -> run ONE warmup turn -> expose GET /healthz on :8080.
"ready" == /healthz answers 200, the same readiness signal used for every subject.
"""

import http.server
import socketserver
from typing import TypedDict

from langgraph.graph import END, START, StateGraph


class State(TypedDict):
    prompt: str
    reply: str


def agent_node(state: State) -> State:
    # Deterministic offline stub — the "model" held constant. No provider call.
    return {"reply": f"ACK (offline stub): {state['prompt'][:40]}"}


builder = StateGraph(State)
builder.add_node("agent", agent_node)
builder.add_edge(START, "agent")
builder.add_edge("agent", END)
graph = builder.compile()

# One warmup turn through the real compiled graph.
graph.invoke({"prompt": "benchmark warmup", "reply": ""})


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        if self.path == "/healthz":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status":"ok"}')
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, *args):  # silence access logs
        pass


if __name__ == "__main__":
    print("ready", flush=True)
    with socketserver.TCPServer(("", 8080), Handler) as httpd:
        httpd.serve_forever()
