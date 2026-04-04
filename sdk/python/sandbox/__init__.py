"""Byggflow Sandbox SDK — Python client for sandboxd.

Usage::

    from sandbox import create_sandbox, connect_sandbox

    sandbox = await create_sandbox()
    result = await sandbox.process.exec_("echo hello")
    print(result.stdout)
    await sandbox.close()
"""

from .auth import Auth, resolve_auth
from .call import CallContext, call
from .env import EnvCategory
from .errors import (
    CapacityError,
    ConnectionError,
    FsError,
    RpcError,
    SandboxError,
    SessionReplacedError,
    TimeoutError,
)
from .fs import FsCategory
from .net import NetCategory
from .process import ExecResult, ProcessCategory, PtyHandle, SpawnHandle
from .sandbox import (
    DEFAULT_ENDPOINT,
    ConnectOptions,
    Sandbox,
    SandboxOptions,
    connect_sandbox,
    create_sandbox,
)
from .template import TemplateCategory, TemplateManager
from .transport import RpcTransport, WsTransport

__all__ = [
    # Entry points
    "create_sandbox",
    "connect_sandbox",
    # Core
    "Sandbox",
    "SandboxOptions",
    "ConnectOptions",
    "DEFAULT_ENDPOINT",
    # Categories
    "FsCategory",
    "ProcessCategory",
    "EnvCategory",
    "NetCategory",
    "TemplateCategory",
    "TemplateManager",
    # Process handles
    "ExecResult",
    "SpawnHandle",
    "PtyHandle",
    # Call infrastructure
    "CallContext",
    "call",
    "RpcTransport",
    "WsTransport",
    # Auth
    "Auth",
    "resolve_auth",
    # Errors
    "SandboxError",
    "ConnectionError",
    "RpcError",
    "TimeoutError",
    "FsError",
    "CapacityError",
    "SessionReplacedError",
]
