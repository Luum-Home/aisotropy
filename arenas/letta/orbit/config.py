"""Environment configuration for the Orbit-on-Letta arena."""

from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class OrbitConfig:
    """Resolved runtime configuration.

    All fields come from the environment. When ``base_url`` is unset the arena
    runs OFFLINE (real Letta types, no server) — this is the default in CI.
    """

    base_url: str | None
    api_key: str | None
    project_id: str | None
    model: str
    port: int

    @property
    def is_live(self) -> bool:
        """True only when a Letta server URL is configured.

        We never fake a server connection: if this is False, callers must use the
        offline memory model (``orbit.memory``) instead of the network client.
        """
        return bool(self.base_url)


def load_config() -> OrbitConfig:
    return OrbitConfig(
        base_url=os.environ.get("LETTA_BASE_URL"),
        api_key=os.environ.get("LETTA_API_KEY"),
        project_id=os.environ.get("LETTA_PROJECT_ID"),
        # Letta model handle form: "<provider>/<model>". openai/gpt-4o-mini is a
        # common default; only used on the LIVE path (needs a provider key on the
        # server side).
        model=os.environ.get("LETTA_MODEL", "openai/gpt-4o-mini"),
        port=int(os.environ.get("ORBIT_PORT", "4115")),
    )
