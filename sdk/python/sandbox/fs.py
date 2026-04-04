"""Filesystem category — ``sandbox.fs``."""

from __future__ import annotations

from typing import Any, Dict, List, Union

from .call import CallContext, call


class FsCategory:
    """File-system operations inside a sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def read(self, path: str) -> bytes:
        """Read a file and return its contents as bytes."""
        result = await call(self._ctx, "fs.read", {"path": path})
        if isinstance(result, bytes):
            return result
        if isinstance(result, str):
            return result.encode()
        return bytes(result)

    async def write(self, path: str, content: Union[str, bytes]) -> None:
        """Write *content* to *path*, creating or overwriting the file."""
        payload: Dict[str, Any] = {"path": path}
        if isinstance(content, str):
            payload["content"] = content
        else:
            payload["content"] = content.decode("utf-8", errors="surrogateescape")
        await call(self._ctx, "fs.write", payload)

    async def list(self, path: str) -> List[str]:
        """List entries in a directory."""
        result = await call(self._ctx, "fs.list", {"path": path})
        return list(result)

    async def stat(self, path: str) -> Dict[str, Any]:
        """Return metadata for *path*."""
        return await call(self._ctx, "fs.stat", {"path": path})

    async def remove(self, path: str) -> None:
        """Remove a file or directory."""
        await call(self._ctx, "fs.remove", {"path": path})

    async def mkdir(self, path: str) -> None:
        """Create a directory (and parents)."""
        await call(self._ctx, "fs.mkdir", {"path": path})

    async def upload(self, path: str, tar: bytes) -> None:
        """Upload a tar archive and extract it at *path*."""
        await call(self._ctx, "fs.upload", {"path": path, "tar": tar})

    async def download(self, path: str) -> bytes:
        """Download *path* as a tar archive."""
        result = await call(self._ctx, "fs.download", {"path": path})
        if isinstance(result, bytes):
            return result
        return bytes(result)
