"""Network category — ``sandbox.net``."""

from __future__ import annotations

from typing import Any, Dict, Optional

from .call import CallContext, call


class NetCategory:
    """Network operations proxied through the sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def fetch(
        self,
        url: str,
        *,
        method: str = "GET",
        headers: Optional[Dict[str, str]] = None,
        body: Optional[str | bytes] = None,
    ) -> Dict[str, Any]:
        """Perform an HTTP request from inside the sandbox.

        Returns a dict with ``status``, ``headers``, and ``body`` keys.
        The exact shape will be finalised once the transport is implemented.
        """
        params: Dict[str, Any] = {"url": url, "method": method}
        if headers is not None:
            params["headers"] = headers
        if body is not None:
            params["body"] = body if isinstance(body, str) else body.decode("utf-8", errors="surrogateescape")
        return await call(self._ctx, "net.fetch", params)
