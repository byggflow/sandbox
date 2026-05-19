# @byggflow/sandbox

TypeScript SDK for [sandboxd](https://github.com/byggflow/sandbox) -- create and manage isolated sandboxes with filesystem, process execution, environment variable, and network access. Works on Node, Bun, and Deno.

## Install

```sh
# npm
npm install @byggflow/sandbox

# JSR (Deno, Bun, or npm via npx)
npx jsr add @byggflow/sandbox
```

## Quick start

```ts
import { createSandbox } from "@byggflow/sandbox";

// Auto-discovers the daemon: SANDBOXD_ENDPOINT env var, then the Unix socket
// at /var/run/sandboxd/sandboxd.sock, then http://localhost:7522.
const sbx = await createSandbox();

await sbx.fs.write("/root/main.py", "print('hello')");
const result = await sbx.process.exec("python /root/main.py");
console.log(result.stdout); // "hello\n"

await sbx.close();
```

For remote deployments, pass an explicit endpoint:

```ts
const sbx = await createSandbox({
  endpoint: "https://sandbox.example.com",
  auth: "your-api-token",
});
```

### Environment variables

| Variable | Effect |
|---|---|
| `SANDBOXD_ENDPOINT` | Default endpoint when no `endpoint` option is passed. |
| `SBX_AUTH` | Default bearer token when no `auth` option is passed. |

Explicit options always win over the environment.

## Streaming output

```ts
const handle = sbx.process.streamExec("python /root/train.py");
for await (const event of handle.output) {
  process.stdout.write(event.data);
}
const code = await handle.exitCode;
```

## Port tunneling

```ts
// Path-based proxy URL (no allocation, works immediately)
const url = sbx.net.url(3000);

// Dedicated host port (waits for port readiness)
const tunnel = await sbx.net.expose(8080);
console.log(tunnel.url); // "http://host:assigned-port"

const ports = await sbx.net.ports();
await sbx.net.close(8080);
```

## Network middleware

Inject credentials, allow, or deny outbound HTTP requests with rules evaluated on the daemon. Credentials injected via `setHeaders` never enter the sandbox process memory.

```ts
const sbx = await createSandbox({
  network: {
    egress: [
      {
        match: { host: "api.openai.com" },
        action: "inject",
        inject: { setHeaders: { Authorization: `Bearer ${process.env.OPENAI_KEY}` } },
      },
      { match: { host: "*.metadata.google.internal" }, action: "deny" },
    ],
  },
});

// or at runtime:
await sbx.network.inject("api.stripe.com", { Authorization: `Bearer ${stripeKey}` });
await sbx.network.deny("*.evil.test");

// programmatic handler — runs in your SDK process, can mint per-request
// tokens or return a synthetic response:
await sbx.network.defer("api.dynamic.com", async (req) => {
  const token = await mintToken({ url: req.url });
  return { request: { ...req, headers: { ...req.headers, Authorization: `Bearer ${token}` } } };
});
```

Match clauses accept exact hosts, suffix globs (`"*.github.com"`), or regex (`"/^[a-z]+\\.evil\\.test$/"`). The most-specific match wins.

HTTPS is intercepted transparently via a per-sandbox CA that the daemon mints. Leaf certs are signed on demand for each SNI host and cached. The CA private key never leaves the daemon; injected credentials never enter the sandbox.

**Enforcement is cooperative.** Rules apply to HTTP clients that honor `HTTP_PROXY` / `HTTPS_PROXY` (Node fetch, Python requests, Go net/http, curl, ...). Code that opens raw TCP sockets or ignores proxy env vars bypasses the middleware. For untrusted-code scenarios that need a hard guarantee, run the sandbox inside an egress-restricted network namespace. Network middleware is also incompatible with `encrypted: true` — E2E encryption hides RPC params from the daemon, so rules can't be evaluated; `createSandbox` rejects the combination.

## Connect to an existing sandbox

```ts
import { connectSandbox } from "@byggflow/sandbox";

const sbx = await connectSandbox("sandbox-id", {
  endpoint: "https://sandbox.example.com",
  auth: "your-api-token",
});
```

## Templates

```ts
import { templates } from "@byggflow/sandbox";

const mgr = templates({ endpoint: "https://sandbox.example.com", auth: "your-api-token" });
const list = await mgr.list();
```

## Handling capacity errors

When the daemon is at its sandbox limit it returns 429 / 503 and the SDK throws
a `CapacityError` with a `retryAfter` (seconds) hint:

```ts
import { createSandbox, CapacityError } from "@byggflow/sandbox";

async function withRetry<T>(fn: () => Promise<T>, attempts = 3): Promise<T> {
  for (let i = 0; i < attempts; i++) {
    try {
      return await fn();
    } catch (err) {
      if (!(err instanceof CapacityError) || i === attempts - 1) throw err;
      await new Promise((r) => setTimeout(r, err.retryAfter * 1000));
    }
  }
  throw new Error("unreachable");
}

const sbx = await withRetry(() => createSandbox({ profile: "python" }));
```

Port-tunneling errors (`net.expose`, `net.close`, `net.ports`) also surface as
`CapacityError` when the daemon's tunnel pool is exhausted.

## Multi-tenant deployments (signed requests)

Plain bearer tokens are fine for trusted callers. For SaaS deployments where a
reverse proxy sits in front of sandboxd and authenticates end users, use
`signatureAuth()` to sign each request with an Ed25519 key:

```ts
import { createSandbox, signatureAuth } from "@byggflow/sandbox";

const sbx = await createSandbox({
  endpoint: "https://sandbox.acme.com",
  auth: signatureAuth({
    privateKey,           // 32- or 64-byte Uint8Array
    identity: "user-42",  // becomes X-Sandbox-Identity
    maxConcurrent: 5,     // optional per-request limits
    maxTTL: 3600,
  }),
});
```

The daemon scopes every resource (sandboxes, templates, tunnels) to the signed
identity. See the SECURITY section in the main README for the full key-rotation
flow.

## End-to-end encryption

```ts
const sbx = await createSandbox({ encrypted: true });
```

The SDK and guest agent perform a key exchange (X25519). All payloads are encrypted (AES-256-GCM) before leaving the client, covering command arguments, environment values, file paths, RPC results, and file contents. Binary file transfer frames (used by `fs.read`, `fs.write`, `fs.upload`, `fs.download`) are each independently encrypted with a unique nonce.

## Sandbox capabilities

| Category | Operations |
|---|---|
| **fs** | `read`, `write`, `list`, `stat`, `remove`, `rename`, `mkdir`, `upload`, `download` |
| **process** | `exec`, `streamExec` |
| **env** | `get`, `set`, `delete`, `list` |
| **net** | `fetch`, `url`, `expose`, `close`, `ports` |
| **template** | `save` |

`process.spawn` and `process.pty` are supported by the agent and Go SDK but
are not yet exposed through this TypeScript SDK; see issue tracker for status.

## License

[MIT](https://github.com/byggflow/sandbox/blob/main/LICENSE)
