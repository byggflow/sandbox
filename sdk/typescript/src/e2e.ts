import type { RpcTransport } from "./transport.ts";

/** AES-GCM overhead: 12-byte nonce + 16-byte authentication tag. */
const GCM_OVERHEAD = 28;

/** Transport-level chunk size (must match WsTransport / agent protocol). */
const CHUNK_SIZE = 1024 * 1024; // 1MB

/**
 * Maximum plaintext per chunk so that encrypted output is exactly CHUNK_SIZE.
 * This ensures encrypted chunks align with the transport's 1MB chunking.
 */
const PLAIN_CHUNK_SIZE = CHUNK_SIZE - GCM_OVERHEAD;

/**
 * Negotiate E2E encryption with the agent and return an encrypted transport wrapper.
 *
 * Uses Web Crypto API (available in Bun, Node 19+, and browsers) for:
 * - X25519 ECDH key exchange
 * - AES-256-GCM payload encryption
 */
export async function negotiateE2E(transport: RpcTransport): Promise<RpcTransport> {
  // Generate X25519 keypair.
  const clientKP = (await crypto.subtle.generateKey(
    { name: "X25519" },
    false,
    ["deriveBits"],
  )) as unknown as CryptoKeyPair;

  // Export our public key.
  const clientPubRaw = await crypto.subtle.exportKey("raw", clientKP.publicKey);
  const clientPubB64 = btoa(String.fromCharCode(...new Uint8Array(clientPubRaw)));

  // Send to agent.
  const result = (await transport.call("session.negotiate_e2e", {
    public_key: clientPubB64,
  })) as { public_key: string };

  // Import agent's public key.
  const agentPubBytes = Uint8Array.from(atob(result.public_key), (c) => c.charCodeAt(0));
  const agentPub = await crypto.subtle.importKey(
    "raw",
    agentPubBytes,
    { name: "X25519" },
    false,
    [],
  );

  // Derive shared secret via ECDH.
  const sharedBits = await crypto.subtle.deriveBits(
    { name: "X25519", public: agentPub },
    clientKP.privateKey,
    256,
  );

  // Import as AES-GCM key.
  const aesKey = await crypto.subtle.importKey(
    "raw",
    sharedBits,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );

  return new EncryptedTransport(transport, aesKey);
}

class EncryptedTransport implements RpcTransport {
  constructor(
    private inner: RpcTransport,
    private key: CryptoKey,
  ) {}

  async call(method: string, params: unknown): Promise<unknown> {
    const encrypted = await this.encryptParams(params);
    const result = await this.inner.call(method, encrypted);
    return this.decryptResult(result);
  }

  async callWithBinary(method: string, params: unknown, data: Uint8Array): Promise<unknown> {
    // Encrypt each plaintext chunk independently so encrypted chunks align
    // with the transport's 1MB frame boundaries.
    const encryptedChunks: Uint8Array[] = [];
    for (let offset = 0; offset < data.byteLength; offset += PLAIN_CHUNK_SIZE) {
      const end = Math.min(offset + PLAIN_CHUNK_SIZE, data.byteLength);
      encryptedChunks.push(await this.encryptBinary(data.subarray(offset, end)));
    }
    if (encryptedChunks.length === 0) {
      encryptedChunks.push(await this.encryptBinary(new Uint8Array(0)));
    }

    // Concatenate encrypted chunks into a single blob for the inner transport.
    let totalLen = 0;
    for (const c of encryptedChunks) totalLen += c.byteLength;
    const encryptedData = new Uint8Array(totalLen);
    let pos = 0;
    for (const c of encryptedChunks) {
      encryptedData.set(c, pos);
      pos += c.byteLength;
    }

    // Adjust chunking params to match encrypted chunk count.
    const adjustedParams = adjustBinaryParams(params, encryptedChunks.length);
    const encryptedParams = await this.encryptParams(adjustedParams);

    const result = await this.inner.callWithBinary(method, encryptedParams, encryptedData);
    return this.decryptResult(result);
  }

  async callExpectBinary(method: string, params: unknown): Promise<{ result: unknown; binary: Uint8Array[] }> {
    const encrypted = await this.encryptParams(params);
    const { result, binary } = await this.inner.callExpectBinary(method, encrypted);
    const decrypted = await this.decryptResult(result);
    // Each binary buffer is an independently encrypted chunk.
    const decryptedBinary = await Promise.all(
      binary.map((buf) => this.decryptBinary(buf)),
    );
    return { result: decrypted, binary: decryptedBinary };
  }

  async notify(method: string, params: unknown): Promise<void> {
    const encrypted = await this.encryptParams(params);
    this.inner.notify(method, encrypted);
  }

  onNotification(handler: (method: string, params: unknown) => void): void {
    this.inner.onNotification(async (method, params) => {
      try {
        const decrypted = await this.decryptResult(params);
        handler(method, decrypted);
      } catch {
        handler(method, params);
      }
    });
  }

  onReplaced(handler: () => void): void {
    this.inner.onReplaced(handler);
  }

  sendBinary(data: Uint8Array): void {
    this.inner.sendBinary(data);
  }

  onBinary(handler: (data: Uint8Array) => void): void {
    this.inner.onBinary(handler);
  }

  async close(): Promise<void> {
    await this.inner.close();
  }

  private async encryptParams(params: unknown): Promise<{ _encrypted: string }> {
    const plaintext = new TextEncoder().encode(JSON.stringify(params));
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv },
      this.key,
      plaintext,
    );
    // Prepend IV to ciphertext (same layout as Go: nonce + ciphertext).
    const combined = new Uint8Array(iv.length + ciphertext.byteLength);
    combined.set(iv);
    combined.set(new Uint8Array(ciphertext), iv.length);
    return { _encrypted: btoa(String.fromCharCode(...combined)) };
  }

  private async decryptResult(result: unknown): Promise<unknown> {
    if (
      typeof result !== "object" ||
      result === null ||
      !("_encrypted" in result)
    ) {
      return result;
    }
    const encoded = (result as { _encrypted: string })._encrypted;
    const combined = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0));
    const iv = combined.slice(0, 12);
    const ciphertext = combined.slice(12);
    const plaintext = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv },
      this.key,
      ciphertext,
    );
    return JSON.parse(new TextDecoder().decode(plaintext));
  }

  /** Encrypt raw binary data with AES-256-GCM. Returns [12-byte IV][ciphertext+tag]. */
  private async encryptBinary(data: Uint8Array): Promise<Uint8Array> {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv },
      this.key,
      data,
    );
    const combined = new Uint8Array(iv.length + ciphertext.byteLength);
    combined.set(iv);
    combined.set(new Uint8Array(ciphertext), iv.length);
    return combined;
  }

  /** Decrypt binary data encrypted by encryptBinary. */
  private async decryptBinary(data: Uint8Array): Promise<Uint8Array> {
    const iv = data.slice(0, 12);
    const ciphertext = data.slice(12);
    const plaintext = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv },
      this.key,
      ciphertext,
    );
    return new Uint8Array(plaintext);
  }
}

/**
 * Adjust binary transfer params to reflect the encrypted chunk count.
 * Keeps the original `size` (matching actual plaintext data) but updates
 * `chunked` and `chunks` to match the number of encrypted frames.
 */
function adjustBinaryParams(params: unknown, encryptedChunks: number): unknown {
  if (typeof params !== "object" || params === null) return params;
  const p = { ...(params as Record<string, unknown>) };
  if (encryptedChunks > 1) {
    p.chunked = true;
    p.chunks = encryptedChunks;
  }
  return p;
}
