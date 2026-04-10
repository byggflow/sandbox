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

// Connects to /var/run/sandboxd/sandboxd.sock by default
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

## End-to-end encryption

```ts
const sbx = await createSandbox({ encrypted: true });
```

The SDK and guest agent perform a key exchange (X25519). All payloads are encrypted (AES-256-GCM) before leaving the client. The daemon forwards opaque blobs and cannot read file contents, command arguments, or environment values.

## Sandbox capabilities

| Category | Operations |
|---|---|
| **fs** | `read`, `write`, `list`, `stat`, `remove`, `rename`, `mkdir`, `upload`, `download` |
| **process** | `exec`, `streamExec`, `spawn`, `pty` |
| **env** | `get`, `set`, `delete`, `list` |
| **net** | `fetch`, `url`, `expose`, `close`, `ports` |
| **template** | `save` |

## License

[MIT](https://github.com/byggflow/sandbox/blob/main/LICENSE)
