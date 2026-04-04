"""Central RPC call backbone.

Every category method delegates to ``call()`` so that validation,
credential injection, and error mapping happen in exactly one place.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict

from .errors import RpcError, SandboxError
from .transport import RpcTransport


@dataclass(frozen=True)
class CallContext:
    """Immutable context threaded through every category."""

    sandbox_id: str
    transport: RpcTransport


async def call(ctx: CallContext, op: str, params: Dict[str, Any] | None = None) -> Any:
    """Perform a single RPC call through the transport.

    Parameters
    ----------
    ctx:
        The call context (sandbox id + transport).
    op:
        Operation name, e.g. ``"fs.read"``.
    params:
        Operation-specific payload.  Defaults to an empty dict.

    Returns
    -------
    Any
        The decoded response payload from the daemon.

    Raises
    ------
    RpcError
        If the daemon returns an error response.
    SandboxError
        For unexpected transport-level failures.
    """
    if params is None:
        params = {}

    payload: Dict[str, Any] = {
        "sandbox_id": ctx.sandbox_id,
        **params,
    }

    try:
        return await ctx.transport.send(op, payload)
    except RpcError:
        raise
    except SandboxError:
        raise
    except Exception as exc:
        raise SandboxError(f"call {op} failed: {exc}") from exc
