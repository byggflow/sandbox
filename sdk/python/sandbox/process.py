"""Process category — ``sandbox.process``."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, AsyncIterator, Dict, Optional

from .call import CallContext, call


@dataclass
class ExecResult:
    """Result of a blocking process execution."""

    stdout: str
    stderr: str
    exit_code: int


class SpawnHandle:
    """Handle for a long-running spawned process.

    Stream attributes and control methods are stubs until the transport
    layer is implemented.
    """

    def __init__(self, ctx: CallContext, pid: int) -> None:
        self._ctx = ctx
        self.pid = pid

    async def stdout(self) -> AsyncIterator[bytes]:
        """Async iterator over stdout chunks."""
        raise NotImplementedError("transport not yet implemented")
        # Make the function an async generator so the type checks out.
        yield b""  # pragma: no cover

    async def stderr(self) -> AsyncIterator[bytes]:
        """Async iterator over stderr chunks."""
        raise NotImplementedError("transport not yet implemented")
        yield b""  # pragma: no cover

    async def kill(self, signal: str = "SIGTERM") -> None:
        """Send a signal to the process."""
        raise NotImplementedError("transport not yet implemented")

    async def wait(self) -> int:
        """Wait for the process to exit and return the exit code."""
        raise NotImplementedError("transport not yet implemented")


class PtyHandle:
    """Handle for a pseudo-terminal session.

    Control methods are stubs until the transport layer is implemented.
    """

    def __init__(self, ctx: CallContext, pid: int) -> None:
        self._ctx = ctx
        self.pid = pid

    async def data(self) -> AsyncIterator[bytes]:
        """Async iterator over PTY output."""
        raise NotImplementedError("transport not yet implemented")
        yield b""  # pragma: no cover

    async def write(self, data: str | bytes) -> None:
        """Write input to the PTY."""
        raise NotImplementedError("transport not yet implemented")

    async def resize(self, cols: int, rows: int) -> None:
        """Resize the PTY."""
        raise NotImplementedError("transport not yet implemented")

    async def kill(self, signal: str = "SIGTERM") -> None:
        """Send a signal to the PTY process."""
        raise NotImplementedError("transport not yet implemented")

    async def wait(self) -> int:
        """Wait for the PTY process to exit and return the exit code."""
        raise NotImplementedError("transport not yet implemented")


class ProcessCategory:
    """Process execution inside a sandbox."""

    def __init__(self, ctx: CallContext) -> None:
        self._ctx = ctx

    async def exec_(
        self,
        command: str,
        *,
        env: Optional[Dict[str, str]] = None,
        timeout: Optional[int] = None,
    ) -> ExecResult:
        """Run *command* and wait for it to finish.

        This is named ``exec_`` to avoid shadowing the Python builtin.
        """
        params: Dict[str, Any] = {"command": command}
        if env is not None:
            params["env"] = env
        if timeout is not None:
            params["timeout"] = timeout
        result = await call(self._ctx, "process.exec", params)
        return ExecResult(
            stdout=result.get("stdout", ""),
            stderr=result.get("stderr", ""),
            exit_code=result.get("exitCode", result.get("exit_code", -1)),
        )

    # Alias so callers can use ``process.exec(...)`` when the name is accessed
    # as an attribute (e.g. ``getattr(process, 'exec')``).
    exec = exec_

    async def spawn(
        self,
        command: str,
        *,
        env: Optional[Dict[str, str]] = None,
    ) -> SpawnHandle:
        """Start a long-running process and return a handle."""
        params: Dict[str, Any] = {"command": command}
        if env is not None:
            params["env"] = env
        result = await call(self._ctx, "process.spawn", params)
        pid: int = result.get("pid", 0)
        return SpawnHandle(self._ctx, pid)

    async def pty(
        self,
        *,
        command: Optional[str] = None,
        cols: int = 80,
        rows: int = 24,
        env: Optional[Dict[str, str]] = None,
    ) -> PtyHandle:
        """Open a pseudo-terminal session."""
        params: Dict[str, Any] = {"cols": cols, "rows": rows}
        if command is not None:
            params["command"] = command
        if env is not None:
            params["env"] = env
        result = await call(self._ctx, "process.pty", params)
        pid: int = result.get("pid", 0)
        return PtyHandle(self._ctx, pid)
