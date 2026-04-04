"""Tests for auth resolution."""

import pytest

from sandbox.auth import resolve_auth


@pytest.mark.asyncio
async def test_resolve_none():
    headers = await resolve_auth(None)
    assert headers == {}


@pytest.mark.asyncio
async def test_resolve_bearer_token():
    headers = await resolve_auth("my-token")
    assert headers == {"Authorization": "Bearer my-token"}


@pytest.mark.asyncio
async def test_resolve_dict():
    custom = {"X-Api-Key": "abc123"}
    headers = await resolve_auth(custom)
    assert headers == custom


@pytest.mark.asyncio
async def test_resolve_async_callable():
    async def provider():
        return {"Authorization": "Bearer dynamic"}

    headers = await resolve_auth(provider)
    assert headers == {"Authorization": "Bearer dynamic"}
