"""Tests that the public API surface is importable and well-formed."""

import inspect

import pytest


def test_top_level_imports():
    from byggflow.sandbox import (
        Auth,
        CallContext,
        CapacityError,
        ConnectOptions,
        ConnectionError,
        DEFAULT_ENDPOINT,
        EnvCategory,
        ExecResult,
        FsCategory,
        FsError,
        NetCategory,
        ProcessCategory,
        PtyHandle,
        RpcError,
        RpcTransport,
        Sandbox,
        SandboxError,
        SandboxOptions,
        SessionReplacedError,
        SpawnHandle,
        TemplateCategory,
        TemplateManager,
        TimeoutError,
        call,
        connect_sandbox,
        create_sandbox,
        resolve_auth,
    )


def test_default_endpoint():
    from byggflow.sandbox import DEFAULT_ENDPOINT

    assert DEFAULT_ENDPOINT == "unix:///var/run/sandboxd/sandboxd.sock"


def test_create_sandbox_is_async():
    from byggflow.sandbox import create_sandbox

    assert inspect.iscoroutinefunction(create_sandbox)


def test_connect_sandbox_is_async():
    from byggflow.sandbox import connect_sandbox

    assert inspect.iscoroutinefunction(connect_sandbox)


def test_sandbox_options_defaults():
    from byggflow.sandbox import SandboxOptions

    opts = SandboxOptions()
    assert opts.endpoint == "unix:///var/run/sandboxd/sandboxd.sock"
    assert opts.auth is None
    assert opts.profile is None
    assert opts.encrypted is False


def test_connect_options_defaults():
    from byggflow.sandbox import ConnectOptions

    opts = ConnectOptions()
    assert opts.endpoint == "unix:///var/run/sandboxd/sandboxd.sock"
    assert opts.retry is False


def test_exec_result_fields():
    from byggflow.sandbox import ExecResult

    r = ExecResult(stdout="hi", stderr="", exit_code=0)
    assert r.stdout == "hi"
    assert r.exit_code == 0


def test_sandbox_has_category_properties():
    """Verify the Sandbox class exposes the expected category properties."""
    from byggflow.sandbox import Sandbox

    props = {name for name, val in inspect.getmembers(Sandbox) if isinstance(val, property)}
    assert {"fs", "process", "env", "net", "template", "id"}.issubset(props)


def test_rpc_transport_is_abstract():
    from byggflow.sandbox import RpcTransport

    assert inspect.isabstract(RpcTransport)


@pytest.mark.asyncio
async def test_create_sandbox_raises_without_daemon():
    from byggflow.sandbox import create_sandbox
    from byggflow.sandbox.errors import SandboxError

    with pytest.raises(Exception):
        await create_sandbox()


@pytest.mark.asyncio
async def test_connect_sandbox_raises_without_daemon():
    from byggflow.sandbox import connect_sandbox

    with pytest.raises(Exception):
        await connect_sandbox("sbx-abc123")
