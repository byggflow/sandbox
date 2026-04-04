"""End-to-end encryption: X25519 key exchange + AES-256-GCM payload encryption."""

from __future__ import annotations

import base64
import json
import os
from typing import Any, Callable, Optional

from cryptography.hazmat.primitives.asymmetric.x25519 import (
    X25519PrivateKey,
    X25519PublicKey,
)
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from .transport import RpcTransport


async def negotiate_e2e(transport: RpcTransport) -> "EncryptedTransport":
    """Perform X25519 key exchange and return an encrypted transport wrapper."""
    # Generate client keypair.
    private_key = X25519PrivateKey.generate()
    public_key = private_key.public_key()
    client_pub_b64 = base64.b64encode(
        public_key.public_bytes_raw()
    ).decode()

    # Send our public key to the agent.
    result = await transport.call(
        "session.negotiate_e2e",
        {"public_key": client_pub_b64},
    )

    # Parse agent's public key.
    agent_pub_bytes = base64.b64decode(result["public_key"])
    agent_pub = X25519PublicKey.from_public_bytes(agent_pub_bytes)

    # Derive shared secret.
    shared_secret = private_key.exchange(agent_pub)

    return EncryptedTransport(transport, shared_secret)


class EncryptedTransport(RpcTransport):
    """Wraps an RpcTransport with AES-256-GCM encryption on all payloads."""

    def __init__(self, inner: RpcTransport, shared_secret: bytes) -> None:
        self._inner = inner
        self._gcm = AESGCM(shared_secret)

    async def call(self, method: str, params: Any = None) -> Any:
        encrypted = self._encrypt_params(params)
        result = await self._inner.call(method, encrypted)
        return self._decrypt_result(result)

    async def notify(self, method: str, params: Any = None) -> None:
        encrypted = self._encrypt_params(params)
        await self._inner.notify(method, encrypted)

    def on_notification(
        self, handler: Callable[[str, Any], None]
    ) -> None:
        def wrapper(method: str, params: Any) -> None:
            try:
                decrypted = self._decrypt_result(params)
                handler(method, decrypted)
            except Exception:
                handler(method, params)

        self._inner.on_notification(wrapper)

    async def close(self) -> None:
        await self._inner.close()

    def _encrypt_params(self, params: Any) -> dict:
        plaintext = json.dumps(params).encode()
        nonce = os.urandom(12)
        ciphertext = self._gcm.encrypt(nonce, plaintext, None)
        # Same layout as Go: nonce + ciphertext.
        combined = nonce + ciphertext
        return {"_encrypted": base64.b64encode(combined).decode()}

    def _decrypt_result(self, result: Any) -> Any:
        if not isinstance(result, dict) or "_encrypted" not in result:
            return result
        combined = base64.b64decode(result["_encrypted"])
        nonce = combined[:12]
        ciphertext = combined[12:]
        plaintext = self._gcm.decrypt(nonce, ciphertext, None)
        return json.loads(plaintext)
