# Deployment Guide

This guide covers deploying sandboxd in production.

## Requirements

- Linux host (x86_64 or arm64)
- Docker Engine 24+
- (Optional) Firecracker + KVM-capable host for microVM sandboxes

## Installation

### Option 1: Install script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh
```

This installs `sandboxd` and `sbx` to `/usr/local/bin`. To install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh -s -- --version v0.1.0
```

### Option 2: Download binary

Download the archive for your platform from the [releases page](https://github.com/byggflow/sandbox/releases):

```bash
# Linux x86_64
curl -fsSL https://github.com/byggflow/sandbox/releases/latest/download/sandbox-v0.1.0-linux-amd64.tar.gz | tar xz
sudo mv sandboxd sbx /usr/local/bin/

# Linux ARM64
curl -fsSL https://github.com/byggflow/sandbox/releases/latest/download/sandbox-v0.1.0-linux-arm64.tar.gz | tar xz
sudo mv sandboxd sbx /usr/local/bin/

# macOS Apple Silicon
curl -fsSL https://github.com/byggflow/sandbox/releases/latest/download/sandbox-v0.1.0-darwin-arm64.tar.gz | tar xz
sudo mv sandboxd sbx /usr/local/bin/
```

### Option 3: Build from source

```bash
git clone https://github.com/byggflow/sandbox.git
cd sandbox
make build
sudo cp bin/sandboxd bin/sbx /usr/local/bin/
```

### Option 4: Docker

```bash
docker pull ghcr.io/byggflow/sandboxd:latest
```

## Configuration

Create a configuration file at `/etc/sandboxd/config.toml`. See [`config/sandboxd.example.toml`](../config/sandboxd.example.toml) for the full reference.

Minimal configuration:

```toml
[server]
socket = "/var/run/sandboxd/sandboxd.sock"
tcp = "0.0.0.0:7522"

[limits]
max_sandboxes = 100
max_memory = "4g"
max_cpu = 4.0
max_ttl = 86400

[pool]
total_warm = 30
health_interval = "10s"

[pool.base.default]
image = "ghcr.io/byggflow/sandbox-base:latest"
memory = "512m"
cpu = 1.0
storage = "500m"
```

## Running with systemd

Copy the service file and start the daemon:

```bash
sudo mkdir -p /etc/sandboxd
sudo cp config/sandboxd.example.toml /etc/sandboxd/config.toml
sudo cp deploy/sandboxd.service /etc/systemd/system/

# Edit the config
sudo nano /etc/sandboxd/config.toml

sudo systemctl daemon-reload
sudo systemctl enable sandboxd
sudo systemctl start sandboxd
```

Check status:

```bash
sudo systemctl status sandboxd
sudo journalctl -u sandboxd -f
sbx health
```

## Running with Docker Compose

```bash
mkdir -p /etc/sandboxd
cp config/sandboxd.example.toml /etc/sandboxd/config.toml
cp deploy/docker-compose.yml /etc/sandboxd/

cd /etc/sandboxd
docker compose up -d
```

Verify the daemon is running:

```bash
curl --unix-socket /var/run/sandboxd/sandboxd.sock http://localhost/health
# or if TCP is enabled:
curl http://localhost:7522/health
```

## Pulling sandbox images

The daemon needs sandbox images available locally. Pull them before starting:

```bash
docker pull ghcr.io/byggflow/sandbox-base:latest
docker pull ghcr.io/byggflow/sandbox-full:latest
docker pull ghcr.io/byggflow/sandbox-node:latest
docker pull ghcr.io/byggflow/sandbox-python:latest
```

Reference these images in your pool configuration:

```toml
[pool.base.default]
image = "ghcr.io/byggflow/sandbox-base:latest"

[pool.base.python]
image = "ghcr.io/byggflow/sandbox-python:latest"

[pool.base.node]
image = "ghcr.io/byggflow/sandbox-node:latest"

[pool.base.full]
image = "ghcr.io/byggflow/sandbox-full:latest"
```

## Multi-tenant setup

For SaaS deployments, enable identity scoping with Ed25519 signature verification:

```toml
[multi_tenant]
enabled = true
public_keys = [
  "base64-encoded-ed25519-public-key-1",
  "base64-encoded-ed25519-public-key-2",
]
```

Your reverse proxy (nginx, Caddy, etc.) authenticates users and signs requests before forwarding to sandboxd. See the [Security section](../README.md#access-control) in the README for details.

## TLS

For direct TCP exposure (without a reverse proxy):

```toml
[server]
tcp = "0.0.0.0:7522"
tls_cert = "/etc/sandboxd/tls/cert.pem"
tls_key = "/etc/sandboxd/tls/key.pem"
```

## Firewall

sandboxd needs:

| Port | Direction | Purpose |
|---|---|---|
| 7522 (or custom) | Inbound | API (if TCP is enabled) |
| Tunnel port range | Inbound | Exposed sandbox ports (default 30000-39999) |
| 443, 80 | Outbound | Pulling images, sandbox internet access |

## Monitoring

sandboxd exposes Prometheus metrics at `/metrics` and a health check at `/health`.

```bash
# Health check
curl http://localhost:7522/health

# Prometheus metrics
curl http://localhost:7522/metrics

# Pool status (Unix socket only)
curl --unix-socket /var/run/sandboxd/sandboxd.sock http://localhost/pools
```

### Prometheus scrape config

```yaml
scrape_configs:
  - job_name: sandboxd
    static_configs:
      - targets: ["localhost:7522"]
```

## Upgrading

1. Download the new version.
2. Stop the daemon (`systemctl stop sandboxd` or `docker compose down`).
3. Replace the binaries.
4. Start the daemon.

Configuration is hot-reloaded on file change or `SIGHUP`. Listen addresses require a restart; all other settings apply live.

## Troubleshooting

**Daemon won't start**
- Check Docker is running: `docker info`
- Check the config file is valid TOML: `sandboxd --config /etc/sandboxd/config.toml --validate`
- Check logs: `journalctl -u sandboxd -e`

**Sandboxes fail to create**
- Verify sandbox images are pulled: `docker images | grep sandbox`
- Check resource limits aren't exhausted: `sbx pool status`
- Check Docker disk space: `docker system df`

**Connection refused**
- Verify the socket exists: `ls -la /var/run/sandboxd/sandboxd.sock`
- Verify TCP is enabled in config if connecting over the network
- Check firewall rules
