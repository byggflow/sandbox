import { ed25519 } from "@noble/curves/ed25519.js";

export type Auth =
  | string
  | Record<string, string>
  | (() => Promise<Record<string, string>>)
  | RequestSignerAuth;

/** Auth provider that produces per-request signatures based on HTTP method and path. */
export interface RequestSignerAuth {
  resolveForRequest(method: string, path: string): Promise<Record<string, string>>;
}

export function isRequestSigner(auth: Auth | undefined): auth is RequestSignerAuth {
  return auth != null && typeof auth === "object" && "resolveForRequest" in auth;
}

export function resolveAuth(auth: Auth | undefined): () => Promise<Record<string, string>> {
  if (!auth) return async () => ({});
  if (typeof auth === "string") return async () => ({ Authorization: `Bearer ${auth}` });
  if (typeof auth === "function") return auth;
  if (isRequestSigner(auth)) {
    // Fallback for contexts that don't support per-request signing.
    return async () => {
      throw new Error("SignatureAuth requires per-request signing context");
    };
  }
  return async () => auth as Record<string, string>;
}

const SIGNED_HEADERS = [
  "X-Sandbox-Identity",
  "X-Sandbox-Max-Concurrent",
  "X-Sandbox-Max-TTL",
  "X-Sandbox-Max-Templates",
  "X-Sandbox-Timestamp",
] as const;

export interface SignatureAuthOptions {
  /** Ed25519 private key (64 bytes) as Uint8Array. */
  privateKey: Uint8Array;
  /** Tenant identity value. */
  identity: string;
  /** Optional per-request limit overrides. */
  maxConcurrent?: number;
  maxTTL?: number;
  maxTemplates?: number;
}

/** Creates a SignatureAuth provider that signs requests with an Ed25519 private key. */
export function signatureAuth(opts: SignatureAuthOptions): RequestSignerAuth {
  return {
    async resolveForRequest(method: string, path: string): Promise<Record<string, string>> {
      const headers: Record<string, string> = {
        "X-Sandbox-Identity": opts.identity,
        "X-Sandbox-Timestamp": Math.floor(Date.now() / 1000).toString(),
      };
      if (opts.maxConcurrent && opts.maxConcurrent > 0) {
        headers["X-Sandbox-Max-Concurrent"] = opts.maxConcurrent.toString();
      }
      if (opts.maxTTL && opts.maxTTL > 0) {
        headers["X-Sandbox-Max-TTL"] = opts.maxTTL.toString();
      }
      if (opts.maxTemplates && opts.maxTemplates > 0) {
        headers["X-Sandbox-Max-Templates"] = opts.maxTemplates.toString();
      }

      // Build payload: method\npath\nheader1\nheader2\n...
      const parts = [method, path];
      for (const h of SIGNED_HEADERS) {
        parts.push(headers[h] ?? "");
      }
      const payload = new TextEncoder().encode(parts.join("\n"));

      // Sign with Ed25519. @noble/curves expects the 32-byte seed (first half of 64-byte key).
      const seed = opts.privateKey.length === 64 ? opts.privateKey.slice(0, 32) : opts.privateKey;
      const signature = ed25519.sign(payload, seed);

      // Base64 encode the signature.
      const sigB64 = btoa(String.fromCharCode(...signature));
      headers["X-Sandbox-Signature"] = sigB64;

      return headers;
    },
  };
}
