import type { RpcTransport } from "./transport.ts";

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
    const encrypted = await this.encryptParams(params);
    const result = await this.inner.callWithBinary(method, encrypted, data);
    return this.decryptResult(result);
  }

  async callExpectBinary(method: string, params: unknown): Promise<{ result: unknown; binary: Uint8Array[] }> {
    const encrypted = await this.encryptParams(params);
    const { result, binary } = await this.inner.callExpectBinary(method, encrypted);
    const decrypted = await this.decryptResult(result);
    return { result: decrypted, binary };
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
}
