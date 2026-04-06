"""Process category — ``sandbox.process``."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Any, AsyncIterator, Dict, Optional

from .call import CallContext, call


@dataclass
class ExecResult:
    """Result of a blocking process execution."""

    stdout: str
    stderr: str
    exit_code: int


@dataclass
class OutputEvent:
    """A single output event from a streaming process."""

    stream: str  # "stdout" or "stderr"
    data: str


class StreamExecHandle:
    """Handle for a streaming process execution.

    Iterate asynchronously to receive output events, then await
    ``exit_code()`` to get the final status.
    """

    def __init__(
        self,
        output: asyncio.Queue[Optional[OutputEvent]],
        exit_code_future: asyncio.Future[int],
    ) -> None:
        self._output = output
        self._exit_code_future = exit_code_future

    async def __aiter__(self):
        while True:
            event = await self._output.get()
            if event is None:
                break
            yield event

    async def exit_code(self) -> int:
        """Wait for the process to exit and return the exit code."""
        return await self._exit_code_future


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

    async def stream_exec(
        self,
        command: str,
        *,
        env: Optional[Dict[str, str]] = None,
        timeout: Optional[int] = None,
        cwd: Optional[str] = None,
    ) -> StreamExecHandle:
        """Run *command* with streaming output.

        Returns a ``StreamExecHandle`` that yields ``OutputEvent`` items
        and exposes an ``exit_code()`` awaitable.
        """
        output: asyncio.Queue[Optional[OutputEvent]] = asyncio.Queue()
        loop = asyncio.get_running_loop()
        exit_code_future: asyncio.Future[int] = loop.create_future()

        def _on_notification(method: str, params: Any) -> None:
            if method != "process.output":
                return
            if params is None:
                return
            stream = params.get("stream", "stdout")
            data = params.get("data", "")
            if data:
                output.put_nowait(OutputEvent(stream=stream, data=data))

        self._ctx.transport.on_notification(_on_notification)

        rpc_params: Dict[str, Any] = {"command": command}
        if env is not None:
            rpc_params["env"] = env
        if timeout is not None:
            rpc_params["timeout"] = timeout
        if cwd is not None:
            rpc_params["cwd"] = cwd

        async def _run() -> None:
            try:
                result = await call(self._ctx, "process.stream", rpc_params)
                output.put_nowait(None)
                if not exit_code_future.done():
                    code = result.get("exit_code", -1)
                    exit_code_future.set_result(int(code))
            except Exception as exc:
                output.put_nowait(None)
                if not exit_code_future.done():
                    exit_code_future.set_exception(exc)

        asyncio.create_task(_run())
        return StreamExecHandle(output, exit_code_future)

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
