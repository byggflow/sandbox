"""Authentication helpers.

Auth can be:
- ``str``  — treated as a Bearer token.
- ``dict[str, str]``  — used directly as headers.
- An async callable returning ``dict[str, str]`` — called each time headers are needed.
- A ``RequestSigner`` instance for per-request Ed25519 signing.
- ``None``  — no authentication.
"""

from __future__ import annotations

import base64
import time
from dataclasses import dataclass, field
from typing import Awaitable, Callable, Dict, Optional, Protocol, Union, runtime_checkable

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


@runtime_checkable
class RequestSigner(Protocol):
    """Auth provider that produces per-request signatures based on HTTP method and path."""

    async def resolve_for_request(self, method: str, path: str) -> Dict[str, str]: ...


Auth = Union[str, Dict[str, str], Callable[[], Awaitable[Dict[str, str]]], RequestSigner, None]

_SIGNED_HEADERS = [
    "X-Sandbox-Identity",
    "X-Sandbox-Max-Concurrent",
    "X-Sandbox-Max-TTL",
    "X-Sandbox-Max-Templates",
    "X-Sandbox-Timestamp",
]


async def resolve_auth(auth: Auth) -> Dict[str, str]:
    """Resolve an *Auth* value into a concrete header dict."""
    if auth is None:
        return {}
    if isinstance(auth, str):
        return {"Authorization": f"Bearer {auth}"}
    if isinstance(auth, dict):
        return dict(auth)
    if isinstance(auth, RequestSigner):
        raise TypeError("SignatureAuth requires per-request signing; use resolve_for_request()")
    # Must be an async callable.
    return await auth()


@dataclass
class SignatureAuth:
    """Ed25519 signature auth for multi-tenant mode.

    Signs each request with the provided private key, setting identity
    and limit headers that the daemon's verifier can validate.

    Usage::

        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

        key = Ed25519PrivateKey.from_private_bytes(raw_key_bytes)
        auth = SignatureAuth(private_key=key, identity="tenant-1")
        sandbox = await create_sandbox(SandboxOptions(auth=auth))
    """

    private_key: Ed25519PrivateKey
    identity: str
    max_concurrent: int = 0
    max_ttl: int = 0
    max_templates: int = 0

    async def resolve_for_request(self, method: str, path: str) -> Dict[str, str]:
        """Build identity headers and sign them with the Ed25519 private key."""
        headers: Dict[str, str] = {
            "X-Sandbox-Identity": self.identity,
            "X-Sandbox-Timestamp": str(int(time.time())),
        }
        if self.max_concurrent > 0:
            headers["X-Sandbox-Max-Concurrent"] = str(self.max_concurrent)
        if self.max_ttl > 0:
            headers["X-Sandbox-Max-TTL"] = str(self.max_ttl)
        if self.max_templates > 0:
            headers["X-Sandbox-Max-Templates"] = str(self.max_templates)

        # Build payload: method\npath\nheader1\nheader2\n...
        parts = [method, path]
        for h in _SIGNED_HEADERS:
            parts.append(headers.get(h, ""))
        payload = "\n".join(parts).encode()

        sig = self.private_key.sign(payload)
        headers["X-Sandbox-Signature"] = base64.b64encode(sig).decode()

        return headers
