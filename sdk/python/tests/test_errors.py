"""Tests for the error hierarchy."""

from sandbox.errors import (
    CapacityError,
    ConnectionError,
    FsError,
    RpcError,
    SandboxError,
    SessionReplacedError,
    TimeoutError,
)


def test_sandbox_error_is_base():
    err = SandboxError("boom")
    assert isinstance(err, Exception)
    assert str(err) == "boom"
    assert err.message == "boom"


def test_connection_error_inherits_sandbox_error():
    err = ConnectionError("lost")
    assert isinstance(err, SandboxError)
    assert str(err) == "lost"


def test_rpc_error_has_code():
    err = RpcError("bad request", code=400)
    assert isinstance(err, SandboxError)
    assert err.code == 400
    assert str(err) == "bad request"


def test_timeout_error():
    err = TimeoutError("took too long")
    assert isinstance(err, SandboxError)
    assert str(err) == "took too long"


def test_fs_error_has_fs_code():
    err = FsError("not found", fs_code="ENOENT")
    assert isinstance(err, SandboxError)
    assert err.fs_code == "ENOENT"


def test_capacity_error_has_retry_after():
    err = CapacityError("full", retry_after=5.0)
    assert isinstance(err, SandboxError)
    assert err.retry_after == 5.0


def test_session_replaced_error():
    err = SessionReplacedError()
    assert isinstance(err, ConnectionError)
    assert isinstance(err, SandboxError)
    assert "replaced" in str(err).lower()
