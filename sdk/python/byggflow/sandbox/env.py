"""Environment-variable category — ``sandbox.env``."""

from __future__ import annotations

from typing import Any, Dict, Optional

from .call import CallContext, call


class EnvCategory:
    """Environment variable operations inside a sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def get(self, key: str) -> Optional[str]:
        """Return the value of *key*, or ``None`` if unset."""
        result = await call(self._ctx, "env.get", {"key": key})
        return result if isinstance(result, str) else None

    async def set(self, key: str, value: str) -> None:
        """Set *key* to *value*."""
        await call(self._ctx, "env.set", {"key": key, "value": value})

    async def delete(self, key: str) -> None:
        """Delete *key*."""
        await call(self._ctx, "env.delete", {"key": key})

    async def list(self) -> Dict[str, str]:
        """Return all environment variables as a dict."""
        result = await call(self._ctx, "env.list")
        return dict(result) if result else {}
