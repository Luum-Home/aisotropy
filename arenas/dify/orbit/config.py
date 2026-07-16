"""Orbit config for the Dify arena.

Live mode targets a REAL running Dify server via its REST API (the only way to
actually run Dify — there is no in-process/embedded library). Offline mode (the
default here) runs the source-grounded isolation simulation with no server.
"""

from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass
class Config:
    base_url: str | None
    api_key: str | None

    @property
    def is_live(self) -> bool:
        # A real Dify deployment exposes /v1 with a per-app API key. Without both
        # a URL and a key there is nothing to talk to — Dify is a server.
        return bool(self.base_url and self.api_key)


def load_config() -> Config:
    return Config(
        base_url=os.environ.get("DIFY_API_URL"),  # e.g. http://localhost/v1
        api_key=os.environ.get("DIFY_API_KEY"),   # per-app key: app-xxxxxxxx
    )
