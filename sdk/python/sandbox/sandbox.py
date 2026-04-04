"""Core sandbox lifecycle — ``create_sandbox()``, ``connect_sandbox()``, ``Sandbox``."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict, Optional

import httpx

from .auth import Auth, resolve_auth
from .call import CallContext
from .env import EnvCategory
from .errors import ConnectionError
from .fs import FsCategory
from .net import NetCategory
from .process import ProcessCategory
from .template import TemplateCategory
from .transport import RpcTransport, WsTransport

DEFAULT_ENDPOINT = "unix:///var/run/sandboxd/sandboxd.sock"


@dataclass
class SandboxOptions:
    """Options for creating a new sandbox."""

    endpoint: str = DEFAULT_ENDPOINT
    auth: Auth = None
    image: Optional[str] = None
    template: Optional[str] = None
    memory: Optional[str] = None
    cpu: Optional[float] = None
    ttl: Optional[int] = None
    encrypted: bool = False


@dataclass
class ConnectOptions:
    """Options for connecting to an existing sandbox."""

    endpoint: str = DEFAULT_ENDPOINT
    auth: Auth = None
    encrypted: bool = False
    retry: bool = False


class Sandbox:
    """A connected sandbox instance.

    Access capabilities through category properties:

    - ``sandbox.fs``  — filesystem operations
    - ``sandbox.process``  — process execution
    - ``sandbox.env``  — environment variables
    - ``sandbox.net``  — network proxying
    - ``sandbox.template``  — template snapshotting
    """

    def __init__(self, sandbox_id: str, transport: RpcTransport) -> None:
        self._id = sandbox_id
        self._transport = transport
        self._ctx = CallContext(sandbox_id=sandbox_id, transport=transport)

        self._fs = FsCategory(self._ctx)
        self._process = ProcessCategory(self._ctx)
        self._env = EnvCategory(self._ctx)
        self._net = NetCategory(self._ctx)
        self._template = TemplateCategory(self._ctx)

    @property
    def id(self) -> str:
        """The sandbox identifier (``sbx-...``)."""
        return self._id

    @property
    def fs(self) -> FsCategory:
        """Filesystem operations."""
        return self._fs

    @property
    def process(self) -> ProcessCategory:
        """Process execution."""
        return self._process

    @property
    def env(self) -> EnvCategory:
        """Environment variables."""
        return self._env

    @property
    def net(self) -> NetCategory:
        """Network proxy."""
        return self._net

    @property
    def template(self) -> TemplateCategory:
        """Template snapshotting."""
        return self._template

    async def close(self) -> None:
        """Disconnect from the sandbox."""
        await self._transport.close()


def _resolve_endpoints(endpoint: str) -> tuple[str, str]:
    """Return (http_base, ws_base) from the raw endpoint string."""
    if endpoint.startswith("unix://"):
        return ("http://localhost:7522", "ws://localhost:7522")

    http_base = endpoint.rstrip("/")
    ws_base = http_base.replace("https://", "wss://").replace("http://", "ws://")
    return (http_base, ws_base)


async def create_sandbox(opts: Optional[SandboxOptions] = None) -> Sandbox:
    """Create a new sandbox and return a connected ``Sandbox`` instance."""
    opts = opts or SandboxOptions()
    headers = await resolve_auth(opts.auth)
    http_base, ws_base = _resolve_endpoints(opts.endpoint)

    body: Dict[str, Any] = {}
    if opts.image is not None:
        body["image"] = opts.image
    if opts.template is not None:
        body["template"] = opts.template
    if opts.memory is not None:
        body["memory"] = opts.memory
    if opts.cpu is not None:
        body["cpu"] = opts.cpu
    if opts.ttl is not None:
        body["ttl"] = opts.ttl

    async with httpx.AsyncClient() as client:
        response = await client.post(
            f"{http_base}/sandboxes",
            json=body,
            headers={"Content-Type": "application/json", **headers},
        )

    if response.status_code not in (200, 201):
        raise ConnectionError(f"Failed to create sandbox: {response.status_code} {response.text}")

    data = response.json()
    sandbox_id: str = data["id"]

    transport = WsTransport()
    await transport.connect(f"{ws_base}/sandboxes/{sandbox_id}/ws", headers)

    return Sandbox(sandbox_id, transport)


async def connect_sandbox(
    sandbox_id: str,
    opts: Optional[ConnectOptions] = None,
) -> Sandbox:
    """Connect to an existing sandbox by id."""
    opts = opts or ConnectOptions()
    headers = await resolve_auth(opts.auth)
    _, ws_base = _resolve_endpoints(opts.endpoint)

    transport = WsTransport()
    await transport.connect(f"{ws_base}/sandboxes/{sandbox_id}/ws", headers)

    return Sandbox(sandbox_id, transport)
