"""Template category — ``sandbox.template`` and standalone ``templates()``."""

from __future__ import annotations

from typing import Any, Dict, List, Optional

import httpx

from .auth import Auth, RequestSigner, resolve_auth
from .call import CallContext, call


class TemplateCategory:
    """Template operations scoped to a sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def save(self, *, label: Optional[str] = None) -> Dict[str, str]:
        """Snapshot the current sandbox as a template.

        Returns a dict containing at least ``{"id": "tpl-..."}``
        """
        params: Dict[str, Any] = {}
        if label is not None:
            params["label"] = label
        return await call(self._ctx, "template.save", params)


class TemplateManager:
    """Standalone template manager (not scoped to a single sandbox).

    Returned by the top-level ``templates()`` factory.
    Uses httpx to communicate with the daemon's REST API.
    """

    def __init__(self, endpoint: str, auth: Auth = None) -> None:
        self._endpoint = endpoint
        self._auth = auth

    def _resolve_http(self) -> str:
        if self._endpoint.startswith("unix://"):
            return "http://localhost:7522"
        return self._endpoint.rstrip("/")

    async def _resolve_headers(self, method: str, path: str) -> Dict[str, str]:
        if isinstance(self._auth, RequestSigner):
            return await self._auth.resolve_for_request(method, path)
        return await resolve_auth(self._auth)

    async def list(self) -> List[Dict[str, Any]]:
        """List available templates."""
        headers = await self._resolve_headers("GET", "/templates")
        http_base = self._resolve_http()
        async with httpx.AsyncClient() as client:
            response = await client.get(f"{http_base}/templates", headers=headers)
        response.raise_for_status()
        return response.json()

    async def get(self, template_id: str) -> Dict[str, Any]:
        """Get a template by id."""
        headers = await self._resolve_headers("GET", f"/templates/{template_id}")
        http_base = self._resolve_http()
        async with httpx.AsyncClient() as client:
            response = await client.get(f"{http_base}/templates/{template_id}", headers=headers)
        response.raise_for_status()
        return response.json()

    async def delete(self, template_id: str) -> None:
        """Delete a template by id."""
        headers = await self._resolve_headers("DELETE", f"/templates/{template_id}")
        http_base = self._resolve_http()
        async with httpx.AsyncClient() as client:
            response = await client.delete(f"{http_base}/templates/{template_id}", headers=headers)
        response.raise_for_status()
