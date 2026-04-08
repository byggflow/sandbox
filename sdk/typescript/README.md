# @byggflow/sandbox

TypeScript SDK for creating and managing [Byggflow](https://byggflow.com) sandboxes. Provides filesystem, process execution, environment variable, and network access inside isolated containers.

## Install

```sh
# npm
npx jsr add @byggflow/sandbox

# Deno
deno add jsr:@byggflow/sandbox

# Bun
bunx jsr add @byggflow/sandbox
```

## Quick start

```ts
import { createSandbox } from "@byggflow/sandbox";

const sandbox = await createSandbox({
  endpoint: "https://api.byggflow.com",
  auth: "your-api-token",
});

// Execute a command
const result = await sandbox.process.exec("echo hello");
console.log(result.stdout); // "hello\n"

// Read and write files
await sandbox.fs.write("/tmp/greeting.txt", "Hello, world!");
const content = await sandbox.fs.read("/tmp/greeting.txt");

// Stream process output
const handle = sandbox.process.streamExec("ls -la /tmp");
for await (const event of handle.output) {
  process.stdout.write(event.data);
}

await sandbox.close();
```

## Connect to an existing sandbox

```ts
import { connectSandbox } from "@byggflow/sandbox";

const sandbox = await connectSandbox("sandbox-id", {
  endpoint: "https://api.byggflow.com",
  auth: "your-api-token",
});
```

## Templates

```ts
import { templates } from "@byggflow/sandbox";

const mgr = templates({ endpoint: "https://api.byggflow.com", auth: "your-api-token" });
const list = await mgr.list();
```
