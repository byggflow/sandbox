"""Network category — ``sandbox.net``."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict, List, Optional

import httpx

from .call import CallContext, call


@dataclass
class TunnelInfo:
    """Result of exposing a port."""

    port: int
    host_port: int
    url: str


class NetCategory:
    """Network operations proxied through the sandbox."""

    def __init__(
        self,
        ctx: CallContext,
        http_base: str = "http://localhost:7522",
        auth_headers: Optional[Dict[str, str]] = None,
    ) -> None:
        self._ctx = ctx
        self._http_base = http_base
        self._auth_headers = auth_headers or {}

    async def fetch(
        self,
        url: str,
        *,
        method: str = "GET",
        headers: Optional[Dict[str, str]] = None,
        body: Optional[str | bytes] = None,
    ) -> Dict[str, Any]:
        """Perform an HTTP request from inside the sandbox."""
        params: Dict[str, Any] = {"url": url, "method": method}
        if headers is not None:
            params["headers"] = headers
        if body is not None:
            params["body"] = body if isinstance(body, str) else body.decode("utf-8", errors="surrogateescape")
        return await call(self._ctx, "net.fetch", params)

    def url(self, port: int) -> str:
        """Return a path-based proxy URL for the given container port.

        No server call — the URL is constructed client-side.
        """
        return f"{self._http_base}/sandboxes/{self._ctx.sandbox_id}/ports/{port}"

    async def expose(self, port: int, *, timeout: Optional[int] = None) -> TunnelInfo:
        """Expose a container port with a dedicated host port.

        Waits for the port to accept connections (up to *timeout* seconds, default 30).
        """
        body: Dict[str, Any] = {}
        if timeout is not None:
            body["timeout"] = timeout

        async with httpx.AsyncClient() as client:
            resp = await client.post(
                f"{self._http_base}/sandboxes/{self._ctx.sandbox_id}/ports/{port}/expose",
                json=body,
                headers={"Content-Type": "application/json", **self._auth_headers},
            )

        if resp.status_code != 200:
            raise RuntimeError(f"expose failed (status {resp.status_code}): {resp.text}")

        data = resp.json()
        return TunnelInfo(
            port=data["port"],
            host_port=data["host_port"],
            url=data["url"],
        )

    async def close(self, port: int) -> None:
        """Close an exposed port."""
        async with httpx.AsyncClient() as client:
            resp = await client.delete(
                f"{self._http_base}/sandboxes/{self._ctx.sandbox_id}/ports/{port}/expose",
                headers=self._auth_headers,
            )

        if resp.status_code not in (204, 404):
            raise RuntimeError(f"close failed (status {resp.status_code}): {resp.text}")

    async def ports(self) -> List[TunnelInfo]:
        """List all exposed ports for this sandbox."""
        async with httpx.AsyncClient() as client:
            resp = await client.get(
                f"{self._http_base}/sandboxes/{self._ctx.sandbox_id}/ports",
                headers=self._auth_headers,
            )

        if resp.status_code != 200:
            raise RuntimeError(f"ports failed (status {resp.status_code}): {resp.text}")

        return [
            TunnelInfo(port=p["port"], host_port=p["host_port"], url=p["url"])
            for p in resp.json()
        ]
