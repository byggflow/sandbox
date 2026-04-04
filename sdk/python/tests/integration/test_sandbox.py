"""Integration tests for sandboxd — tests the daemon's HTTP REST API and WebSocket JSON-RPC protocol directly."""

from __future__ import annotations

import json
import os
from typing import Any

import httpx
import pytest
import websockets

ENDPOINT = os.environ.get("SANDBOXD_ENDPOINT", "")

pytestmark = pytest.mark.skipif(not ENDPOINT, reason="SANDBOXD_ENDPOINT not set")


def _ws_url(http_url: str) -> str:
    """Convert an HTTP URL to a WebSocket URL."""
    return http_url.replace("https://", "wss://").replace("http://", "ws://")


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def client() -> httpx.Client:
    """Synchronous HTTP client scoped to the test."""
    with httpx.Client(base_url=ENDPOINT, timeout=30) as c:
        yield c


@pytest.fixture
async def sandbox(client: httpx.Client):
    """Create a sandbox, yield its info dict, and destroy it afterwards."""
    resp = client.post("/sandboxes", json={})
    assert resp.status_code == 201, f"POST /sandboxes returned {resp.status_code}: {resp.text}"
    info = resp.json()
    yield info
    # Cleanup — best effort.
    try:
        client.delete(f"/sandboxes/{info['id']}")
    except Exception:
        pass


@pytest.fixture
async def ws_conn(sandbox: dict[str, Any]):
    """Open a WebSocket connection to the sandbox and yield it."""
    url = f"{_ws_url(ENDPOINT)}/sandboxes/{sandbox['id']}/ws"
    async with websockets.connect(url) as ws:
        yield ws


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


async def send_rpc(
    ws: websockets.WebSocketClientProtocol,
    rpc_id: int,
    method: str,
    params: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Send a JSON-RPC request and return the response, skipping notifications."""
    request = {"jsonrpc": "2.0", "id": rpc_id, "method": method}
    if params is not None:
        request["params"] = params
    await ws.send(json.dumps(request))

    # Read messages until we get a response matching our ID.
    while True:
        msg = await ws.recv()
        if isinstance(msg, bytes):
            # Binary message — skip, the caller handles these separately.
            continue
        data = json.loads(msg)
        if data.get("id") == rpc_id:
            return data


async def read_binary(ws: websockets.WebSocketClientProtocol) -> bytes:
    """Read the next binary WebSocket message, skipping text messages."""
    while True:
        msg = await ws.recv()
        if isinstance(msg, bytes):
            return msg


async def read_text(ws: websockets.WebSocketClientProtocol) -> dict[str, Any]:
    """Read the next text WebSocket message as JSON, skipping binary messages."""
    while True:
        msg = await ws.recv()
        if isinstance(msg, str):
            return json.loads(msg)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_health(client: httpx.Client) -> None:
    resp = client.get("/health")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "ok"


@pytest.mark.asyncio
async def test_create_and_destroy(client: httpx.Client) -> None:
    resp = client.post("/sandboxes", json={})
    assert resp.status_code == 201
    info = resp.json()
    assert info["id"].startswith("sbx-")
    assert info["state"] == "running"

    del_resp = client.delete(f"/sandboxes/{info['id']}")
    assert del_resp.status_code == 204


@pytest.mark.asyncio
async def test_list_sandboxes(client: httpx.Client, sandbox: dict[str, Any]) -> None:
    resp = client.get("/sandboxes")
    assert resp.status_code == 200
    sandbox_list = resp.json()
    ids = [s["id"] for s in sandbox_list]
    assert sandbox["id"] in ids


@pytest.mark.asyncio
async def test_exec_via_websocket(
    ws_conn: websockets.WebSocketClientProtocol,
) -> None:
    resp = await send_rpc(ws_conn, 1, "process.exec", {"command": "echo hello"})
    assert resp.get("error") is None, f"exec error: {resp.get('error')}"
    result = resp["result"]
    assert result["stdout"] == "hello\n"
    assert result["exit_code"] == 0


@pytest.mark.asyncio
async def test_fs_write_and_read(
    ws_conn: websockets.WebSocketClientProtocol,
) -> None:
    test_content = b"hello from python integration test"
    test_path = "/tmp/integration-test.txt"

    # Write: send JSON-RPC request, then binary content.
    write_req = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "fs.write",
        "params": {"path": test_path, "size": len(test_content)},
    }
    await ws_conn.send(json.dumps(write_req))
    await ws_conn.send(test_content)

    # Read the write response.
    write_resp = await read_text(ws_conn)
    assert write_resp.get("error") is None, f"fs.write error: {write_resp.get('error')}"

    # Read the file back.
    read_req = {
        "jsonrpc": "2.0",
        "id": 2,
        "method": "fs.read",
        "params": {"path": test_path},
    }
    await ws_conn.send(json.dumps(read_req))

    # Expect binary content followed by text JSON-RPC response.
    content = await read_binary(ws_conn)
    read_resp = await read_text(ws_conn)

    assert read_resp.get("error") is None, f"fs.read error: {read_resp.get('error')}"
    assert content == test_content


@pytest.mark.asyncio
async def test_env_set_and_get(
    ws_conn: websockets.WebSocketClientProtocol,
) -> None:
    # Set.
    set_resp = await send_rpc(ws_conn, 1, "env.set", {"key": "TEST_VAR", "value": "integration_value"})
    assert set_resp.get("error") is None, f"env.set error: {set_resp.get('error')}"

    # Get.
    get_resp = await send_rpc(ws_conn, 2, "env.get", {"key": "TEST_VAR"})
    assert get_resp.get("error") is None, f"env.get error: {get_resp.get('error')}"

    result = get_resp["result"]
    # Result might be the value directly or wrapped in an object.
    if isinstance(result, str):
        assert result == "integration_value"
    elif isinstance(result, dict):
        assert result.get("value") == "integration_value"
    else:
        pytest.fail(f"Unexpected env.get result type: {type(result)} ({result!r})")


@pytest.mark.asyncio
async def test_destroy_nonexistent(client: httpx.Client) -> None:
    resp = client.delete("/sandboxes/sbx-nonexistent")
    assert resp.status_code == 404


@pytest.mark.asyncio
async def test_create_multiple_sandboxes(client: httpx.Client) -> None:
    created = []
    try:
        for _ in range(3):
            resp = client.post("/sandboxes", json={})
            assert resp.status_code == 201
            created.append(resp.json())

        list_resp = client.get("/sandboxes")
        assert list_resp.status_code == 200
        listed_ids = {s["id"] for s in list_resp.json()}

        for info in created:
            assert info["id"] in listed_ids, f"Sandbox {info['id']} not in list"
    finally:
        for info in created:
            try:
                client.delete(f"/sandboxes/{info['id']}")
            except Exception:
                pass
