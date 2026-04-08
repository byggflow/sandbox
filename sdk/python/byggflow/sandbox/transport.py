"""Transport abstraction for RPC communication with the daemon.

Provides both the abstract ``RpcTransport`` protocol and a concrete
``WsTransport`` implementation backed by the ``websockets`` library.
"""

from __future__ import annotations

import asyncio
import json
from abc import ABC, abstractmethod
from typing import Any, Callable, Dict, List, Optional, Tuple

from .errors import ConnectionError, RpcError, SessionReplacedError

MAX_FRAME_SIZE = 10 * 1024 * 1024  # 10MB
CHUNK_SIZE = 1 * 1024 * 1024  # 1MB


class RpcTransport(ABC):
    """Abstract base for daemon communication."""

    @abstractmethod
    async def send(self, op: str, params: Dict[str, Any]) -> Any:
        """Send an RPC request and return the result payload."""
        ...

    @abstractmethod
    async def call_with_binary(
        self, method: str, params: Dict[str, Any], data: bytes
    ) -> Any:
        """Send a JSON-RPC request followed by binary data chunks, return the JSON response."""
        ...

    @abstractmethod
    async def call_expect_binary(
        self, method: str, params: Dict[str, Any]
    ) -> Tuple[Any, List[bytes]]:
        """Send a JSON-RPC request and collect binary frames before the JSON response."""
        ...

    @abstractmethod
    async def send_binary(self, data: bytes) -> None:
        """Send raw binary data, chunking at CHUNK_SIZE."""
        ...

    @abstractmethod
    async def close(self) -> None:
        """Gracefully shut down the transport."""
        ...


class WsTransport(RpcTransport):
    """WebSocket-based JSON-RPC transport using the ``websockets`` library."""

    def __init__(self) -> None:
        self._ws: Any = None
        self._next_id: int = 1
        self._pending: Dict[int, asyncio.Future[Any]] = {}
        self._notification_handlers: list[Callable[[str, Any], None]] = []
        self._replaced_handlers: list[Callable[[], None]] = []
        self._binary_handler: Optional[Callable[[bytes], None]] = None
        self._read_task: Optional[asyncio.Task[None]] = None
        self._binary_expect_id: int = 0
        self._binary_bufs: Dict[int, list[bytes]] = {}

    async def connect(self, url: str, headers: Optional[Dict[str, str]] = None) -> None:
        """Open a WebSocket connection and start the read loop."""
        import websockets

        extra_headers = headers or {}
        self._ws = await websockets.connect(url, additional_headers=extra_headers)
        self._read_task = asyncio.create_task(self._read_loop())

    async def _read_loop(self) -> None:
        """Background task that reads messages from the WebSocket."""
        try:
            async for message in self._ws:
                if isinstance(message, bytes):
                    # Route to binary-expecting request if one is active.
                    if self._binary_expect_id != 0:
                        req_id = self._binary_expect_id
                        if req_id not in self._binary_bufs:
                            self._binary_bufs[req_id] = []
                        self._binary_bufs[req_id].append(message)
                    elif self._binary_handler is not None:
                        handler = self._binary_handler
                        self._binary_handler = None
                        handler(message)
                    continue

                # Text message — parse JSON-RPC.
                try:
                    msg = json.loads(message)
                except (json.JSONDecodeError, TypeError):
                    continue

                # Response (has id).
                if "id" in msg and msg["id"] is not None:
                    req_id = msg["id"]
                    future = self._pending.pop(req_id, None)
                    if future is None or future.done():
                        continue

                    if "error" in msg and msg["error"]:
                        err = msg["error"]
                        future.set_exception(
                            RpcError(err.get("message", "Unknown error"), err.get("code", -1))
                        )
                    else:
                        future.set_result(msg.get("result"))
                    continue

                # Notification (no id).
                method = msg.get("method")
                if method:
                    if method == "session.replaced":
                        for h in self._replaced_handlers:
                            h()
                        await self._ws.close()
                        return

                    params = msg.get("params")
                    for h in self._notification_handlers:
                        h(method, params)

        except Exception:
            # Connection closed or error — reject all pending requests.
            pass
        finally:
            for future in self._pending.values():
                if not future.done():
                    future.set_exception(ConnectionError("WebSocket closed"))
            self._pending.clear()

    async def send(self, op: str, params: Dict[str, Any]) -> Any:
        """Send a JSON-RPC request and wait for the response."""
        return await self.call(op, params)

    async def call(self, method: str, params: Any = None) -> Any:
        """Send a JSON-RPC request and return the result."""
        if self._ws is None:
            raise ConnectionError("WebSocket not connected")

        req_id = self._next_id
        self._next_id += 1

        loop = asyncio.get_running_loop()
        future: asyncio.Future[Any] = loop.create_future()
        self._pending[req_id] = future

        message = json.dumps({"jsonrpc": "2.0", "id": req_id, "method": method, "params": params})
        await self._ws.send(message)

        return await future

    async def notify(self, method: str, params: Any = None) -> None:
        """Send a JSON-RPC notification (no response expected)."""
        if self._ws is None:
            raise ConnectionError("WebSocket not connected")

        message = json.dumps({"jsonrpc": "2.0", "method": method, "params": params})
        await self._ws.send(message)

    def on_notification(self, handler: Callable[[str, Any], None]) -> None:
        """Register a callback for server-initiated notifications."""
        self._notification_handlers.append(handler)

    def on_replaced(self, handler: Callable[[], None]) -> None:
        """Register a callback for ``session.replaced`` notifications."""
        self._replaced_handlers.append(handler)

    async def send_binary(self, data: bytes) -> None:
        """Send binary data, chunking at CHUNK_SIZE."""
        if self._ws is None:
            raise ConnectionError("WebSocket not connected")
        offset = 0
        while offset < len(data):
            end = min(offset + CHUNK_SIZE, len(data))
            chunk = data[offset:end]
            await self._ws.send(chunk)
            offset = end

    async def call_with_binary(
        self, method: str, params: Dict[str, Any], data: bytes
    ) -> Any:
        """Send a JSON-RPC request, then send binary data in chunks, wait for JSON response."""
        if self._ws is None:
            raise ConnectionError("WebSocket not connected")

        req_id = self._next_id
        self._next_id += 1

        loop = asyncio.get_running_loop()
        future: asyncio.Future[Any] = loop.create_future()
        self._pending[req_id] = future

        message = json.dumps({"jsonrpc": "2.0", "id": req_id, "method": method, "params": params})
        await self._ws.send(message)

        # Send binary data in chunks.
        await self.send_binary(data)

        return await future

    async def call_expect_binary(
        self, method: str, params: Dict[str, Any]
    ) -> Tuple[Any, List[bytes]]:
        """Send a JSON-RPC request and collect binary frames arriving before the JSON response."""
        if self._ws is None:
            raise ConnectionError("WebSocket not connected")

        req_id = self._next_id
        self._next_id += 1

        loop = asyncio.get_running_loop()
        future: asyncio.Future[Any] = loop.create_future()
        self._pending[req_id] = future

        # Tell the read loop to collect binary frames for this request.
        self._binary_expect_id = req_id
        self._binary_bufs[req_id] = []

        message = json.dumps({"jsonrpc": "2.0", "id": req_id, "method": method, "params": params})
        await self._ws.send(message)

        result = await future

        # Collect and clean up binary buffers.
        self._binary_expect_id = 0
        bufs = self._binary_bufs.pop(req_id, [])

        return result, bufs

    def on_binary(self, handler: Callable[[bytes], None]) -> None:
        """Register a one-shot binary message handler."""
        self._binary_handler = handler

    async def close(self) -> None:
        """Close the WebSocket and cancel the read task."""
        if self._read_task is not None:
            self._read_task.cancel()
            try:
                await self._read_task
            except asyncio.CancelledError:
                pass
            self._read_task = None

        if self._ws is not None:
            await self._ws.close()
            self._ws = None

        self._pending.clear()
