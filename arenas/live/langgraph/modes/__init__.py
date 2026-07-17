"""Mode implementations for the LangGraph live-arena adapter.

Each submodule exposes a single ``run(payload: dict) -> dict`` function that
takes the parsed stdin request and returns a complete response envelope
(see ``modes.common.envelope``). ``adapter.py`` dispatches to these based on
the request's ``"mode"`` field.
"""
