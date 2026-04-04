"""Core sandbox lifecycle — ``create_sandbox()``, ``connect_sandbox()``, ``Sandbox``."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict, Optional

import httpx

from .auth import Auth, RequestSigner, resolve_auth
from .call import CallContext
from .env import EnvCategory
from .errors import CapacityError, ConnectionError
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
    profile: Optional[str] = None
    template: Optional[str] = None
    memory: Optional[str] = None
    cpu: Optional[float] = None
    ttl: Optional[int] = None
    labels: Optional[Dict[str, str]] = None
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
    http_base, ws_base = _resolve_endpoints(opts.endpoint)

    signer = opts.auth if isinstance(opts.auth, RequestSigner) else None
    if signer:
        headers = await signer.resolve_for_request("POST", "/sandboxes")
    else:
        headers = await resolve_auth(opts.auth)

    body: Dict[str, Any] = {}
    if opts.profile is not None:
        body["profile"] = opts.profile
    if opts.template is not None:
        body["template"] = opts.template
    if opts.memory is not None:
        body["memory"] = opts.memory
    if opts.cpu is not None:
        body["cpu"] = opts.cpu
    if opts.ttl is not None:
        body["ttl"] = opts.ttl
    if opts.labels is not None:
        body["labels"] = opts.labels

    async with httpx.AsyncClient() as client:
        response = await client.post(
            f"{http_base}/sandboxes",
            json=body,
            headers={"Content-Type": "application/json", **headers},
        )

    if response.status_code in (429, 503):
        retry_after_str = response.headers.get("Retry-After", "60")
        try:
            retry_after = float(retry_after_str)
        except ValueError:
            retry_after = 60.0
        raise CapacityError(response.text, retry_after)
    if response.status_code not in (200, 201):
        raise ConnectionError(f"Failed to create sandbox: {response.status_code} {response.text}")

    data = response.json()
    sandbox_id: str = data["id"]

    ws_headers = (
        await signer.resolve_for_request("GET", f"/sandboxes/{sandbox_id}/ws")
        if signer
        else headers
    )
    ws_transport = WsTransport()
    await ws_transport.connect(f"{ws_base}/sandboxes/{sandbox_id}/ws", ws_headers)

    transport: RpcTransport = ws_transport
    if opts.encrypted:
        from .e2e import negotiate_e2e

        transport = await negotiate_e2e(ws_transport)

    return Sandbox(sandbox_id, transport)


async def connect_sandbox(
    sandbox_id: str,
    opts: Optional[ConnectOptions] = None,
) -> Sandbox:
    """Connect to an existing sandbox by id."""
    opts = opts or ConnectOptions()
    _, ws_base = _resolve_endpoints(opts.endpoint)

    if isinstance(opts.auth, RequestSigner):
        headers = await opts.auth.resolve_for_request("GET", f"/sandboxes/{sandbox_id}/ws")
    else:
        headers = await resolve_auth(opts.auth)

    ws_transport = WsTransport()
    await ws_transport.connect(f"{ws_base}/sandboxes/{sandbox_id}/ws", headers)

    transport: RpcTransport = ws_transport
    if opts.encrypted:
        from .e2e import negotiate_e2e

        transport = await negotiate_e2e(ws_transport)

    return Sandbox(sandbox_id, transport)
