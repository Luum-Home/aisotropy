"""Real Letta client factory (LIVE path only).

This wires the *real* `letta_client.Letta`. It is deliberately the only module
that opens a network connection, and it refuses to run unless a server URL is
configured — we never fake a connection.
"""

from __future__ import annotations

from .config import OrbitConfig

try:
    from letta_client import Letta  # real SDK
except ImportError as exc:  # pragma: no cover - dependency is pinned
    raise ImportError(
        "letta-client is not installed. Run: pip install -r requirements.txt"
    ) from exc


def build_client(cfg: OrbitConfig) -> "Letta":
    """Construct a real Letta client bound to a running server.

    Raises RuntimeError if no server URL is configured — the caller must use the
    offline memory model instead of pretending a server exists.
    """
    if not cfg.is_live:
        raise RuntimeError(
            "No LETTA_BASE_URL configured. Letta agents run on a SERVER; this "
            "environment has none. Use the offline memory model (orbit.memory) "
            "for the isolation smoke, or start `letta server` and set LETTA_BASE_URL."
        )
    # environment="local" for self-hosted `letta server`; the SDK also accepts
    # environment="cloud" + api_key for Letta Cloud (per-project isolation).
    return Letta(
        base_url=cfg.base_url,
        api_key=cfg.api_key or "",
        project_id=cfg.project_id,
        environment="local",
    )


def ping(cfg: OrbitConfig) -> bool:
    """Best-effort liveness check against a real server (LIVE path).

    Returns True only if a real HTTP round-trip to the Letta server succeeds.
    Never returns a fabricated success.
    """
    client = build_client(cfg)
    # models.list is a cheap, read-only server call.
    client.models.list()
    return True
