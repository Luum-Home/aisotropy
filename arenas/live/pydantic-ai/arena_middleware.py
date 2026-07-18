"""Pydantic AI NATIVE model-wrapping middleware -- backs the `middleware` mode.

Pydantic AI ships `pydantic_ai.models.wrapper.WrapperModel` (`pydantic_ai/models/wrapper.py` in
the pinned 2.12.0 install): a `Model` subclass whose entire purpose is to be subclassed and wrap
another `Model`, intercepting `request()` / `request_stream()` before/after the wrapped model's
real call. This is the framework's own model-wrapping seam -- durable-execution integrations
(Temporal/Prefect/DBOS, see the module's own docstring) use the exact same base class -- not a
monkeypatch bolted on from outside.

Two other native options exist and were considered:
  - `pydantic_ai.models.instrumented.InstrumentedModel` -- OBSERVE-only (OTel spans), cannot
    mutate the outbound request. Already exercised honestly by `observability` mode.
  - `Agent(..., history_processors=[...])` / `@agent.system_prompt` -- operate on the
    Pydantic-AI-level message history / prompt BEFORE it is rendered into a model `request()`
    call, one layer above where `WrapperModel` sits. A dynamic `@agent.system_prompt` callback
    regenerates the prompt at render time; it does not intercept the already-built `messages`
    list the way `WrapperModel.request()` does.

`ArenaMiddlewareModel(WrapperModel)` below is honest about which layer it operates at: it mutates
the `messages: list[ModelMessage]` argument of `request()` -- the framework's already-assembled
per-call payload -- not the Agent's `system_prompt` string itself.

  INBOUND: `_inject_sentinel()` appends the sentinel `"\n[ARENA_MW]"` to every `SystemPromptPart`
  found in the (first) `ModelRequest`'s parts, non-destructively (rebuilds new `SystemPromptPart`/
  `ModelRequest` objects via `dataclasses.replace` rather than mutating the caller's history
  objects in place, since those objects may be reused elsewhere -- e.g. a subsequent turn's
  `message_history`). Sets `self.inbound_fired = True` the first time this happens.

  OUTBOUND: `request()` increments `self.request_count` once per real call to the wrapped model,
  AFTER the wrapped model's real response comes back -- proving the wrapper observes every
  outbound request, not just fire-and-forget interception of the inbound side.

Honest scope: only `request()` (the non-streaming path `agent.run()` uses, which is what the
`middleware` mode's one qa turn exercises) is overridden. `request_stream()` is left as
`WrapperModel`'s base-class pass-through (unmodified delegation to `self.wrapped`) -- the
`stream` mode already covers Pydantic AI's real streaming path separately, and this mode does not
stream.
"""

from __future__ import annotations

import dataclasses

from pydantic_ai.messages import ModelMessage, ModelRequest, ModelResponse, SystemPromptPart
from pydantic_ai.models import Model, ModelRequestParameters
from pydantic_ai.models.wrapper import WrapperModel
from pydantic_ai.settings import ModelSettings

from arena_common import log

ARENA_SENTINEL = "\n[ARENA_MW]"


@dataclasses.dataclass(init=False)
class ArenaMiddlewareModel(WrapperModel):
    """WrapperModel that mutates inbound system prompts and counts outbound requests.

    See module docstring for the mechanism and the honest scope note (request() only).
    """

    request_count: int
    inbound_fired: bool

    def __init__(self, wrapped: Model):
        super().__init__(wrapped)
        self.request_count = 0
        self.inbound_fired = False

    def _inject_sentinel(self, messages: list[ModelMessage]) -> list[ModelMessage]:
        """Return a NEW messages list where every SystemPromptPart carries the sentinel.

        Non-destructive: builds new SystemPromptPart/ModelRequest objects via
        dataclasses.replace() instead of mutating the caller's history objects in place -- those
        same objects may be part of a message_history list a caller reuses for a later turn.
        """
        mutated: list[ModelMessage] = []
        for msg in messages:
            if isinstance(msg, ModelRequest):
                new_parts = []
                changed = False
                for part in msg.parts:
                    if isinstance(part, SystemPromptPart) and ARENA_SENTINEL not in part.content:
                        part = dataclasses.replace(part, content=part.content + ARENA_SENTINEL)
                        changed = True
                        self.inbound_fired = True
                    new_parts.append(part)
                mutated.append(dataclasses.replace(msg, parts=new_parts) if changed else msg)
            else:
                mutated.append(msg)
        return mutated

    async def request(
        self,
        messages: list[ModelMessage],
        model_settings: ModelSettings | None,
        model_request_parameters: ModelRequestParameters,
    ) -> ModelResponse:
        mutated = self._inject_sentinel(messages)
        log(
            f"middleware: INBOUND fired={self.inbound_fired} -- system prompt sentinel appended "
            f"before request #{self.request_count + 1}"
        )
        response = await self.wrapped.request(mutated, model_settings, model_request_parameters)
        self.request_count += 1
        log(f"middleware: OUTBOUND observed -- request_count={self.request_count}")
        return response
