#!/usr/bin/env python3
"""Mint a development HS256 tenant JWT for the aisotropy OpenAI-compat shim.

The shim (ADR-0066, POST /v1/chat/completions) derives identity EXCLUSIVELY
from the verified bearer credential: the token minted here is passed AS the
OpenAI api_key (it rides as "Authorization: Bearer <jwt>"), so any OpenAI
client — Pipecat's OpenAILLMService included — authenticates with zero client
modification.

Standard library only (hmac/hashlib/base64/json), mirroring the Go verifier
(internal/auth/jwt.go): claims tenant_id / user_id / exp, HS256 signature over
base64url(header).base64url(claims). The secret MUST match the server's
AISOTROPY_JWT_SECRET and be at least 32 bytes (the verifier fails closed on a
shorter one).

Usage:
    export AISOTROPY_JWT_SECRET='dev-secret-change-me-32-bytes-min!!'
    python3 mint_jwt.py --tenant acme --user alice
    python3 mint_jwt.py --tenant acme --user alice --ttl 86400 --scopes "read:kb write:tasks"
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import sys
import time


def b64url(data: bytes) -> str:
    """base64url without padding, per RFC 7515."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def mint(secret: bytes, tenant: str, user: str, ttl: int, scopes: str) -> str:
    header = {"alg": "HS256", "typ": "JWT"}
    claims: dict[str, object] = {
        "tenant_id": tenant,
        "user_id": user,
        "iat": int(time.time()),
        "exp": int(time.time()) + ttl,
    }
    if scopes:
        # Standard OAuth space-delimited "scope" claim; the Go verifier maps it
        # to verified scopes that gate tool access.
        claims["scope"] = scopes
    signing_input = (
        b64url(json.dumps(header, separators=(",", ":")).encode())
        + "."
        + b64url(json.dumps(claims, separators=(",", ":")).encode())
    )
    sig = hmac.new(secret, signing_input.encode("ascii"), hashlib.sha256).digest()
    return signing_input + "." + b64url(sig)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--tenant", required=True, help="tenant_id claim")
    ap.add_argument("--user", required=True, help="user_id claim")
    ap.add_argument("--ttl", type=int, default=3600, help="token lifetime in seconds (default 3600)")
    ap.add_argument("--scopes", default="", help='optional space-delimited scopes, e.g. "read:kb write:tasks"')
    ap.add_argument(
        "--secret",
        default=os.environ.get("AISOTROPY_JWT_SECRET", ""),
        help="HS256 secret (default: $AISOTROPY_JWT_SECRET)",
    )
    args = ap.parse_args()

    if len(args.secret.encode()) < 32:
        print(
            "error: secret must be at least 32 bytes (matches the server's fail-closed minimum); "
            "set AISOTROPY_JWT_SECRET or pass --secret",
            file=sys.stderr,
        )
        return 2

    print(mint(args.secret.encode(), args.tenant, args.user, args.ttl, args.scopes))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
