# Sandbox

High-frequency container sandboxing as a service.

sandboxd is a daemon that runs alongside Docker, exposes a Unix socket, and makes spinning up isolated environments as cheap and fast as possible. It manages a warm pool of pre-created containers so that creating a sandbox takes single-digit milliseconds instead of seconds.

Built and maintained by [Byggflow](https://byggflow.com).

## Use cases

- AI agent tool-use and code execution
- CI job isolation
- Running untrusted code at high frequency
- Programmable remote machines via SDK

## Quick start

### Prerequisites

- Linux host with Docker installed
- Go 1.25+ (for building from source)

### Install from source

```bash
git clone https://github.com/byggflow/sandbox.git
cd sandbox
make build
```

This produces three binaries in `bin/`:

- `sandboxd` — the daemon
- `sbx` — the CLI
- `sandbox-agent` — the guest agent (baked into sandbox images)

### Run the daemon

```bash
sandboxd --config /etc/sandboxd/config.toml
```

Or with Docker (recommended for production):

```yaml
services:
  sandboxd:
    image: byggflow/sandboxd
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - sandboxd-sock:/var/run/sandboxd
      - sandboxd-data:/var/lib/sandboxd
    environment:
      - SANDBOX_SOCKET=/var/run/sandboxd/sandboxd.sock
      - SANDBOX_TCP=0.0.0.0:7522

volumes:
  sandboxd-sock:
  sandboxd-data:
```

### Create your first sandbox

```bash
sbx create
# sbx-a1b2c3d4

sbx exec sbx-a1b2c3d4 "echo hello"
# hello

sbx rm sbx-a1b2c3d4
```

## Client SDKs

### TypeScript

```bash
npm install @byggflow/sandbox
```

```typescript
import { createSandbox } from "@byggflow/sandbox";

// Connects to /var/run/sandboxd/sandboxd.sock by default
const sbx = await createSandbox();

await sbx.fs.write("/root/main.py", "print('hello')");
const result = await sbx.process.exec("python /root/main.py");
console.log(result.stdout); // "hello\n"

await sbx.close();
```

For remote or SaaS deployments, pass an explicit endpoint:

```typescript
const sbx = await createSandbox({
  endpoint: "https://sandbox.acme.com",
  auth: "sk-abc123",
});
```

### Go

```go
import sandbox "github.com/byggflow/sandbox/sdk/go"

// Connects to /var/run/sandboxd/sandboxd.sock by default
sbx, err := sandbox.Create(ctx, sandbox.Options{})
if err != nil {
    log.Fatal(err)
}
defer sbx.Close(ctx)

err = sbx.FS().Write(ctx, "/root/main.py", []byte("print('hello')"))
result, err := sbx.Process().Exec(ctx, "python /root/main.py")
fmt.Println(result.Stdout) // "hello\n"
```

### MCP server

The MCP server lets AI agents (Claude, etc.) create and interact with sandboxes directly as tool calls.

```bash
npm install @byggflow/sandbox-mcp
```

Add to your MCP client config (e.g. `.mcp.json`):

```json
{
  "mcpServers": {
    "sandbox": {
      "command": "npx",
      "args": ["@byggflow/sandbox-mcp"]
    }
  }
}
```

The MCP server auto-starts a `sandboxd` container via Docker if no daemon is already running. Set `SANDBOX_ENDPOINT` and `SANDBOX_AUTH` to connect to a remote instance instead.

## Architecture

```
CLI / SDK / MCP
    │
    │  HTTP + WebSocket over Unix socket (or TCP)
    ▼
sandboxd (daemon)
    ├── Warm Pool (pre-started containers, <5ms allocation)
    │
    │  Binary-framed protocol over TCP
    ▼
Guest Agent (inside container)
    │
    │  Direct syscalls
    ▼
Filesystem / Processes / Network
```

The daemon manages container lifecycle, warm pools, and WebSocket sessions. The guest agent runs inside each container and handles filesystem operations, process execution, and network requests. Clients never talk directly to containers.

## Warm pool

The core differentiator. Instead of creating a container on every request, the daemon maintains a pool of pre-started containers with the agent already connected. Creating a sandbox from the warm pool takes <5ms.

The pool dynamically allocates slots based on creation frequency across images, with configurable minimums for base images and autoscaling based on hit/miss ratio.

## Sandbox capabilities

| Category | Operations |
|---|---|
| **fs** | `read`, `write`, `list`, `stat`, `remove`, `rename`, `mkdir`, `upload`, `download` |
| **process** | `exec`, `spawn`, `pty` |
| **env** | `get`, `set`, `delete`, `list` |
| **net** | `fetch` |
| **template** | `save` |

## CLI reference

### Sandbox lifecycle

| Command | Description |
|---|---|
| `sbx create` | Create a sandbox, print its ID |
| `sbx create --profile python` | Create from a named pool profile |
| `sbx create --template tpl-a1b2c3` | Create from a saved template |
| `sbx create --memory 1g --cpu 2` | Create with resource limits |
| `sbx create --ttl 3600` | Keep alive for 1 hour after disconnect |
| `sbx ls` | List active sandboxes |
| `sbx rm <id>` | Destroy a sandbox |
| `sbx rm --all` | Destroy all sandboxes |

### Execution

| Command | Description |
|---|---|
| `sbx exec <id> "ls -la"` | Run a command, print output |
| `sbx attach <id>` | Interactive shell session |

### Filesystem

| Command | Description |
|---|---|
| `sbx fs read <id> /path` | Print file to stdout |
| `sbx fs write <id> /path < file` | Write file from stdin |
| `sbx fs ls <id> /path` | List directory |
| `sbx fs upload <id> /path < archive.tar` | Upload tar archive |
| `sbx fs download <id> /path > archive.tar` | Download directory as tar |

### Templates

| Command | Description |
|---|---|
| `sbx tpl save <id> --label my-env` | Snapshot a sandbox as a template |
| `sbx tpl ls` | List templates |
| `sbx tpl rm <id>` | Delete a template |
| `sbx build -f Dockerfile --label my-env .` | Build a template from a Dockerfile |

### Pool management

| Command | Description |
|---|---|
| `sbx pool status` | Show pool counts and hit/miss rates |
| `sbx pool resize default 20` | Adjust warm count for a profile |
| `sbx pool flush python` | Recreate warm containers for a profile |

### Info

| Command | Description |
|---|---|
| `sbx version` | Client and server version |
| `sbx health` | Daemon health and pool status |

## Configuration

See [`config/sandboxd.example.toml`](config/sandboxd.example.toml) for the full reference.

Configuration supports hot-reload on file change or `SIGHUP`. Listen addresses require a restart; all other settings are applied live.

Environment variables override config file values:

| Variable | Config field |
|---|---|
| `SANDBOX_SOCKET` | `server.socket` |
| `SANDBOX_TCP` | `server.tcp` |
| `SANDBOX_DATA_DIR` | `server.data_dir` |

## Security

sandboxd is designed to run untrusted code. Every sandbox is locked down by default — no opt-in required.

### Container hardening

| Control | Detail |
|---|---|
| Read-only rootfs | Writable tmpfs only for `/tmp` and `/root` |
| All capabilities dropped | `--cap-drop ALL` — no privileged operations |
| No new privileges | `--security-opt no-new-privileges` |
| PID limit | 256 by default — prevents fork bombs |
| Tmpfs size caps | `/tmp` 100MB, `/root` 500MB — bounds disk usage per sandbox |
| Memory and CPU caps | Per-sandbox cgroup limits, hard-capped by daemon config |

### Network isolation

Each sandbox runs on a dedicated Docker bridge network (`sandboxd-net`) with inter-container communication disabled. Sandboxes can reach the internet but cannot reach each other. The daemon communicates with agents via container IPs on the bridge — no ports are exposed on the host.

### Access control

sandboxd follows the same model as Docker: if you can reach the socket, you have access. The Unix socket is permission-controlled via file ownership (`root:sandboxd`).

For multi-tenant deployments, a reverse proxy handles authentication (OAuth, API keys, JWTs) and injects an `X-Sandbox-Identity` header. sandboxd scopes all resources — sandboxes, templates, connections — to the caller's identity. Authentication and authorization are independent layers.

### End-to-end encryption

For deployments where the operator should not see data in transit, the SDK supports optional E2E encryption:

```typescript
const sbx = await createSandbox({ encrypted: true });
```

The SDK and guest agent perform a key exchange (X25519) on connect. All payloads are encrypted (AES-256-GCM) before leaving the client. The daemon forwards opaque blobs — it can route requests by method name but cannot read file contents, command arguments, or environment values.


## Development

### Prerequisites

- Go 1.25+
- [Bun](https://bun.sh) (for TypeScript SDK and MCP server)

### Build

```bash
make build          # build all Go binaries
make test           # run Go + TypeScript tests
```

### Test

```bash
# Go
go test ./...

# TypeScript SDK
cd sdk/typescript
bun install
bunx --bun vitest run

# MCP server
cd mcp
bun install
bunx --bun vitest run
```

## Contributing

Contributions are welcome. Please open an issue to discuss your idea before submitting a pull request.

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/my-feature`)
3. Commit using [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, etc.)
4. Push to your fork and open a pull request

## License

[MIT](LICENSE)
