"""Authentication helpers.

Auth can be:
- ``str``  — treated as a Bearer token.
- ``dict[str, str]``  — used directly as headers.
- An async callable returning ``dict[str, str]`` — called each time headers are needed.
- ``None``  — no authentication.
"""

from __future__ import annotations

from typing import Awaitable, Callable, Dict, Optional, Union

Auth = Union[str, Dict[str, str], Callable[[], Awaitable[Dict[str, str]]], None]


async def resolve_auth(auth: Auth) -> Dict[str, str]:
    """Resolve an *Auth* value into a concrete header dict."""
    if auth is None:
        return {}
    if isinstance(auth, str):
        return {"Authorization": f"Bearer {auth}"}
    if isinstance(auth, dict):
        return auth
    # Must be an async callable.
    return await auth()
