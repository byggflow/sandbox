"""Filesystem category — ``sandbox.fs``."""

from __future__ import annotations

from typing import Any, Dict, List, Union

from .call import CallContext, call
from .errors import SandboxError
from .transport import CHUNK_SIZE


class FsCategory:
    """File-system operations inside a sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def read(self, path: str) -> bytes:
        """Read a file and return its contents as bytes."""
        result, bufs = await self._ctx.transport.call_expect_binary(
            "fs.read", {"path": path, "sandbox_id": self._ctx.sandbox_id}
        )
        if not bufs:
            raise SandboxError("no binary data received for fs.read")
        return b"".join(bufs)

    async def write(self, path: str, content: Union[str, bytes]) -> None:
        """Write *content* to *path*, creating or overwriting the file."""
        data = content.encode() if isinstance(content, str) else content
        chunked = len(data) > CHUNK_SIZE
        chunks = (len(data) + CHUNK_SIZE - 1) // CHUNK_SIZE if chunked else 1
        params: Dict[str, Any] = {
            "path": path,
            "size": len(data),
            "sandbox_id": self._ctx.sandbox_id,
        }
        if chunked:
            params["chunked"] = True
            params["chunks"] = chunks
        await self._ctx.transport.call_with_binary("fs.write", params, data)

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
        await self._ctx.transport.call_with_binary(
            "fs.upload",
            {"path": path, "size": len(tar), "sandbox_id": self._ctx.sandbox_id},
            tar,
        )

    async def download(self, path: str) -> bytes:
        """Download *path* as a tar archive."""
        result, bufs = await self._ctx.transport.call_expect_binary(
            "fs.download", {"path": path, "sandbox_id": self._ctx.sandbox_id}
        )
        if not bufs:
            raise SandboxError("no binary data received for fs.download")
        return b"".join(bufs)
