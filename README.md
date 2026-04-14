# Sandbox

[![CI](https://github.com/byggflow/sandbox/actions/workflows/ci.yml/badge.svg)](https://github.com/byggflow/sandbox/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/byggflow/sandbox)](https://github.com/byggflow/sandbox/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

High-frequency sandboxing as a service.

sandboxd is a daemon that exposes a Unix socket and makes spinning up isolated environments as cheap and fast as possible. It supports three isolation levels -- Docker with runc, Docker with gVisor, and Firecracker microVMs -- selectable per profile. A warm pool of pre-created sandboxes means allocation takes single-digit milliseconds instead of seconds.

Built and maintained by [Byggflow](https://byggflow.com).

## Use cases

- AI agent tool-use and code execution
- CI job isolation
- Running untrusted code at high frequency
- Programmable remote machines via SDK

## Installation

### Install script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh
```

This installs `sandboxd` and `sbx` to `/usr/local/bin`. To install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh -s -- --version v0.1.0
```

### Download binary

Grab the archive for your platform from the [releases page](https://github.com/byggflow/sandbox/releases):

| Platform | Download |
|---|---|
| Linux x86_64 | `sandbox-vX.Y.Z-linux-amd64.tar.gz` |
| Linux ARM64 | `sandbox-vX.Y.Z-linux-arm64.tar.gz` |
| macOS x86_64 | `sandbox-vX.Y.Z-darwin-amd64.tar.gz` |
| macOS ARM64 | `sandbox-vX.Y.Z-darwin-arm64.tar.gz` |

```bash
tar xzf sandbox-*.tar.gz
sudo mv sandboxd sbx /usr/local/bin/
```

### Docker

```bash
docker pull ghcr.io/byggflow/sandboxd:latest
```

### Build from source

Requires Go 1.25+.

```bash
git clone https://github.com/byggflow/sandbox.git
cd sandbox
make build
```

This produces three binaries in `bin/`: `sandboxd` (daemon), `sbx` (CLI), and `sandbox-agent` (guest agent baked into sandbox images).

## Quick start

### Prerequisites

- Linux host with Docker installed
- (Optional) [gVisor](https://gvisor.dev/docs/user_guide/install/) for `docker+gvisor` isolation
- (Optional) Firecracker + KVM for microVM sandboxes

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
      # TCP is needed inside Docker for host connectivity; safe here because
      # Docker's network namespace isolates it from the host network.
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

Streaming output as it arrives:

```typescript
const handle = sbx.process.streamExec("python /root/train.py");
for await (const event of handle.output) {
  process.stdout.write(event.data);
}
const code = await handle.exitCode;
```

Port tunneling:

```typescript
// Path-based proxy URL (no allocation, works immediately)
const url = sbx.net.url(3000);

// Dedicated host port (waits for port readiness)
const tunnel = await sbx.net.expose(8080);
console.log(tunnel.url); // "http://host:assigned-port"

const ports = await sbx.net.ports();
await sbx.net.close(8080);
```

### Go

```go
import sandbox "github.com/byggflow/sandbox/sdk/go"

// Connects to /var/run/sandboxd/sandboxd.sock by default
sbx, err := sandbox.Create(ctx, &sandbox.Options{})
if err != nil {
    log.Fatal(err)
}
defer sbx.Close()

err = sbx.FS().Write(ctx, "/root/main.py", []byte("print('hello')"))
result, err := sbx.Process().Exec(ctx, "python /root/main.py", nil)
fmt.Println(result.Stdout) // "hello\n"
```

Streaming output:

```go
stream, err := sbx.Process().StreamExec(ctx, "python /root/train.py", nil)
for event := range stream.Output {
    fmt.Print(event.Data)
}
code, err := stream.ExitCode()
```

Port tunneling:

```go
url := sbx.Net().URL(3000)
tunnel, err := sbx.Net().Expose(ctx, 8080, nil)
ports, err := sbx.Net().Ports(ctx)
err = sbx.Net().Close(ctx, 8080)
```

### Python

```bash
pip install byggflow-sandbox
```

```python
from sandbox import create_sandbox

sbx = await create_sandbox()

await sbx.fs.write("/root/main.py", "print('hello')")
result = await sbx.process.exec_("python /root/main.py")
print(result.stdout)  # "hello\n"

await sbx.close()
```

Streaming output:

```python
handle = await sbx.process.stream_exec("python /root/train.py")
async for event in handle:
    print(event.data, end="")
code = await handle.exit_code()
```

Port tunneling:

```python
url = sbx.net.url(3000)
tunnel = await sbx.net.expose(8080)
ports = await sbx.net.ports()
await sbx.net.close(8080)
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

The MCP server connects to the daemon automatically. It tries, in order: the Unix socket at `/var/run/sandboxd/sandboxd.sock`, TCP on `localhost:7522`, and finally auto-starts a `sandboxd` Docker container as a last resort. Set `SANDBOX_ENDPOINT` and `SANDBOX_AUTH` to connect to a remote instance instead.

Available tools:

| Tool | Description |
|---|---|
| `sandbox_create` | Create a sandbox (profile, template, memory, cpu, ttl) |
| `sandbox_destroy` | Destroy a sandbox |
| `sandbox_list` | List active sandboxes |
| `sandbox_exec` | Run a shell command |
| `sandbox_eval` | Write code to a file and run it |
| `sandbox_read_file` | Read file contents with optional line range |
| `sandbox_write_file` | Create, overwrite, or append to a file |
| `sandbox_edit_file` | Precise edits via `str_replace` or `insert` |
| `sandbox_list_files` | List files with recursive depth |
| `sandbox_upload` | Upload from local path or URL into the sandbox |
| `sandbox_download` | Download a file from the sandbox to the host |
| `sandbox_port_url` | Get a path-based proxy URL for a port |
| `sandbox_expose_port` | Expose a port with a dedicated host port |
| `sandbox_close_port` | Close an exposed port |
| `sandbox_list_ports` | List exposed ports |
| `sandbox_list_templates` | List saved templates |
| `sandbox_create_template` | Snapshot a sandbox into a template |
| `sandbox_list_profiles` | List available profiles |

## Architecture

```
CLI / SDK / MCP
    |
    |  HTTP + WebSocket over Unix socket (or TCP/TLS)
    v
sandboxd (daemon)
    |-- Runtime Interface (Docker+runc, Docker+gVisor, or Firecracker)
    |-- Warm Pool (pre-started sandboxes, <5ms allocation)
    |-- Port Tunnel Manager (path-based proxy + host port allocation)
    |-- Identity Scoping (multi-tenant resource isolation)
    |-- Event Stream (SSE for sandbox lifecycle events)
    |
    |  Binary-framed protocol over TCP (Docker) or vsock (Firecracker)
    v
Guest Agent (inside container or microVM)
    |
    |  Direct syscalls
    v
Filesystem / Processes / Network
```

The daemon manages sandbox lifecycle, warm pools, WebSocket sessions, and port tunneling. The guest agent runs inside each sandbox and handles filesystem operations, process execution, and network requests. Clients never talk directly to sandboxes.

### Runtimes

sandboxd supports three isolation levels, selectable per profile. Each level trades startup speed and overhead for stronger isolation:

| Runtime | Config value | Isolation | Transport | Protects against |
|---|---|---|---|---|
| **Docker + runc** (default) | `docker` | Namespaces, seccomp, dropped caps | TCP over bridge | Process-level escape |
| **Docker + gVisor** | `docker+gvisor` | Userspace kernel, syscall filtering | TCP over bridge | Host kernel exploitation |
| **Firecracker** | `firecracker` | Separate kernel, KVM hardware | vsock (AF_VSOCK) | Kernel-level attacks |

Runtimes are selected per profile in the config. The SDK and API are identical regardless of backend, so callers don't need to know which runtime is in use. gVisor requires [runsc](https://gvisor.dev/docs/user_guide/install/) installed and registered with the Docker daemon.

All resources (sandboxes, templates, tunnels) are scoped to the caller's identity in multi-tenant mode.

## Warm pool

The core differentiator. Instead of creating a sandbox on every request, the daemon maintains a pool of pre-started sandboxes (containers or microVMs) with the agent already connected. Creating a sandbox from the warm pool takes <5ms.

The pool dynamically allocates slots based on creation frequency across profiles, with configurable minimums for base images and autoscaling based on hit/miss ratio. Health checks run on a configurable interval, and unhealthy sandboxes are replaced automatically.

## Sandbox capabilities

| Category | Operations |
|---|---|
| **fs** | `read`, `write`, `list`, `stat`, `remove`, `rename`, `mkdir`, `upload`, `download` |
| **process** | `exec`, `streamExec`, `spawn`, `pty` |
| **env** | `get`, `set`, `delete`, `list` |
| **net** | `fetch`, `url`, `expose`, `close`, `ports` |
| **template** | `save` |

File reads, writes, uploads, and downloads support chunked transfer for large files (>1MB).

## Port tunneling

Sandboxes can expose network ports to the outside through two mechanisms:

**Path-based proxy** -- route traffic through the daemon at `/sandboxes/{id}/ports/{port}/`. No allocation needed; works immediately. Best for API calls, health checks, and programmatic access.

**Host port allocation** -- `POST /sandboxes/{id}/ports/{port}/expose` allocates a dedicated host port and waits for the container port to accept connections. Returns a clean origin URL suitable for web apps, CORS, and browser access. The host port range is configurable via `limits.tunnel_port_min` and `limits.tunnel_port_max`.

Port tunneling is available in all SDKs via the `net` category and in the MCP server via the `sandbox_port_url`, `sandbox_expose_port`, `sandbox_close_port`, and `sandbox_list_ports` tools.

## CLI reference

### Sandbox lifecycle

| Command | Description |
|---|---|
| `sbx create` | Create a sandbox, print its ID |
| `sbx create --profile python` | Create from a named pool profile |
| `sbx create --template tpl-a1b2c3` | Create from a saved template |
| `sbx create --memory 1g --cpu 2` | Create with resource limits |
| `sbx create --ttl 3600` | Keep alive for 1 hour after disconnect |
| `sbx create -l env=prod` | Create with labels (repeatable) |
| `sbx ls` | List active sandboxes |
| `sbx rm <id>` | Destroy a sandbox |
| `sbx rm --all` | Destroy all sandboxes |
| `sbx stats <id>` | Show CPU, memory, network, PID stats |

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
| `sbx update` | Update sbx to the latest release |
| `sbx update --check` | Check for updates without installing |

## Configuration

See [`config/sandboxd.example.toml`](config/sandboxd.example.toml) for the full reference.

Configuration supports hot-reload on file change or `SIGHUP`. Listen addresses require a restart; all other settings are applied live.

Environment variables override config file values:

| Variable | Config field |
|---|---|
| `SANDBOX_SOCKET` | `server.socket` |
| `SANDBOX_TCP` | `server.tcp` |
| `SANDBOX_DATA_DIR` | `server.data_dir` |

Key config sections:

| Section | Fields |
|---|---|
| `server` | `socket`, `tcp`, `tls_cert`, `tls_key`, `data_dir`, `node_id` |
| `multi_tenant` | `enabled`, `public_keys` (Ed25519, supports rotation) |
| `limits` | `max_sandboxes`, `max_memory`, `max_cpu`, `max_ttl`, `max_templates`, `max_template_size`, `template_expiry_days`, `rate_limit_entries`, `max_tunnels`, `max_connections_per_tunnel`, `tunnel_port_min`, `tunnel_port_max` |
| `network` | `bridge_name` |
| `pool` | `total_warm`, `min_per_image`, `min_base`, `max_warm`, `rebalance_window`, `health_interval`, `liveness_timeout` |
| `pool.base.<name>` | `image`, `memory`, `cpu`, `storage`, `runtime` (`docker`, `docker+gvisor`, or `firecracker`) |
| `firecracker` | `binary_path`, `kernel_path`, `rootfs_dir`, `vsock_cid_base` |

## Docker images

Published to GitHub Container Registry:

| Image | Description |
|---|---|
| `ghcr.io/byggflow/sandboxd` | The daemon |
| `ghcr.io/byggflow/sandbox-base` | Alpine with the guest agent (default pool image) |
| `ghcr.io/byggflow/sandbox-full` | Ubuntu 24.04 with common dev tools |
| `ghcr.io/byggflow/sandbox-node` | Node.js 22 (Alpine) |
| `ghcr.io/byggflow/sandbox-python` | Python 3.13 (Alpine) |

All sandbox images include the guest agent binary. Use `pool.base.<name>.image` in the config to reference them.

## Security

sandboxd is designed to run untrusted code. Every sandbox is locked down by default -- no opt-in required.

### Container hardening (Docker + runc)

| Control | Detail |
|---|---|
| Read-only rootfs | Writable tmpfs only for `/tmp` and `/root` |
| All capabilities dropped | `--cap-drop ALL` -- no privileged operations |
| No new privileges | `--security-opt no-new-privileges` |
| PID limit | 256 by default -- prevents fork bombs |
| Tmpfs size caps | `/tmp` 100MB, `/root` configurable (default 500MB) |
| Memory and CPU caps | Per-sandbox cgroup limits, hard-capped by daemon config |

### gVisor isolation (Docker + gVisor)

All container hardening controls above apply, plus:

| Control | Detail |
|---|---|
| Userspace kernel | Syscalls are handled by gVisor's Sentry, not the host kernel |
| Syscall filtering | Only ~380 syscalls implemented; unknown syscalls are rejected |
| Defense in depth | Even if application code has a kernel exploit, it targets the Sentry, not the host |

Set `runtime = "docker+gvisor"` on a profile. Requires [runsc](https://gvisor.dev/docs/user_guide/install/) registered with the Docker daemon.

### microVM isolation (Firecracker)

| Control | Detail |
|---|---|
| Hardware virtualization | Each sandbox is a dedicated KVM virtual machine |
| Minimal attack surface | Firecracker has ~50k lines of Rust, no BIOS/USB/PCI |
| vsock communication | Host-guest communication via AF_VSOCK, no network bridge |
| CoW rootfs | Each VM gets a copy-on-write disk image |
| Memory and vCPU caps | Configured per-profile in the daemon config |

### Network isolation

**Docker (runc and gVisor)**: Each sandbox runs on a dedicated Docker bridge network (`sandboxd-net`) with inter-container communication disabled. Sandboxes can reach the internet but cannot reach each other. The daemon communicates with agents via container IPs on the bridge -- no ports are exposed on the host.

**Firecracker**: Each microVM is fully isolated at the hardware level. The daemon communicates with the guest agent via vsock (AF_VSOCK) with no TCP networking between host and guest.

### Access control

sandboxd follows the same model as Docker: if you can reach the socket, you have access. The Unix socket is permission-controlled via file ownership (`root:sandboxd`).

For multi-tenant deployments, sandboxd supports Ed25519 signature verification. A reverse proxy handles authentication (OAuth, API keys, JWTs), signs requests with Ed25519, and injects an `X-Sandbox-Identity` header. sandboxd verifies the signature and scopes all resources -- sandboxes, templates, tunnels -- to the caller's identity. Multiple public keys are supported for zero-downtime key rotation.

Admin routes (pool management) are restricted to Unix socket connections only and are not accessible over TCP.

### End-to-end encryption

For deployments where the operator should not see data in transit, the SDK supports optional E2E encryption:

```typescript
const sbx = await createSandbox({ encrypted: true });
```

The SDK and guest agent perform a key exchange (X25519) on connect. All payloads are encrypted (AES-256-GCM) before leaving the client -- the daemon can route requests by method name but cannot read command arguments, environment values, file paths, RPC results, or file contents. This covers both JSON-RPC params/results and binary file transfer frames (used by `fs.read`, `fs.write`, `fs.upload`, `fs.download`). Each binary frame is independently encrypted with a unique nonce.

## API endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/sandboxes` | Create a sandbox |
| `GET` | `/sandboxes` | List sandboxes |
| `DELETE` | `/sandboxes/{id}` | Destroy a sandbox |
| `GET` | `/sandboxes/{id}/stats` | Resource usage stats |
| `GET` | `/sandboxes/{id}/ws` | WebSocket session |
| `POST` | `/templates` | Create a template |
| `GET` | `/templates` | List templates |
| `GET` | `/templates/{id}` | Get template details |
| `DELETE` | `/templates/{id}` | Delete a template |
| `GET` | `/profiles` | List pool profiles |
| `POST` | `/sandboxes/{id}/ports/{port}/expose` | Expose a port |
| `DELETE` | `/sandboxes/{id}/ports/{port}/expose` | Close an exposed port |
| `GET` | `/sandboxes/{id}/ports` | List exposed ports |
| `*` | `/sandboxes/{id}/ports/{port}/` | Reverse proxy to container port |
| `GET` | `/events` | SSE event stream |
| `GET` | `/events/history` | Recent event history |
| `GET` | `/pools` | Pool status (Unix socket only) |
| `PUT` | `/pools/{profile}` | Resize pool (Unix socket only) |
| `POST` | `/pools/{profile}/flush` | Flush pool (Unix socket only) |
| `GET` | `/health` | Health check (no auth) |
| `GET` | `/metrics` | Prometheus metrics (no auth) |

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

## Deployment

See the [Deployment Guide](docs/deployment.md) for production setup with systemd, Docker Compose, TLS, monitoring, and multi-tenant configuration.

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, code style, testing, and PR guidelines.

## Security

For reporting vulnerabilities, see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
