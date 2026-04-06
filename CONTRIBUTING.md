# Contributing to Sandbox

Thank you for your interest in contributing to Sandbox. This guide will help you get started.

## Getting started

### Prerequisites

- Go 1.25+
- [Bun](https://bun.sh) (TypeScript SDK and MCP server)
- Python 3.13+ (Python SDK)
- Docker (for integration tests)

### Clone and build

```bash
git clone https://github.com/byggflow/sandbox.git
cd sandbox
make build
```

### Run tests

```bash
make test              # all unit tests (Go + TypeScript + Python)
make test-go           # Go only
make test-ts           # TypeScript SDK only
make test-py           # Python SDK only
make test-integration  # full integration suite (requires Docker)
```

## Making changes

### 1. Open an issue first

Before starting work on a new feature or significant change, please open an issue to discuss it. This helps avoid duplicate work and ensures alignment on direction.

Bug fixes and small improvements can go straight to a pull request.

### 2. Branch naming

Create a branch from `main`:

```bash
git checkout -b feat/my-feature    # new feature
git checkout -b fix/my-bugfix      # bug fix
git checkout -b docs/my-docs       # documentation
```

### 3. Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). Every commit message must follow this format:

```
<type>[optional scope]: <description>
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `perf`, `chore`.

Optional scopes: `daemon`, `agent`, `sdk`, `cli`, `mcp`, `runtime`, `pool`, `api`.

Examples:

```
feat(sdk): add batch execution method
fix(daemon): prevent race in pool rebalance
docs: add deployment guide
test(agent): add filesystem edge case tests
```

### 4. Code style

**Go**
- Follow standard Go conventions. Run `go vet ./...` before committing.
- Use `context.Context` as the first parameter for I/O functions.
- Errors are values. Wrap with `fmt.Errorf("doing x: %w", err)`.
- Minimize dependencies. Prefer the standard library.

**TypeScript**
- Use Bun as the runtime.
- Test with vitest (`bunx --bun vitest run`).

**Python**
- Use type hints.
- Test with pytest.

### 5. Testing

All pull requests must pass CI. Please include tests for new functionality. The test suite runs automatically on every PR.

- **Unit tests**: cover the new code path.
- **Integration tests**: required for changes to the daemon, agent protocol, or SDK client logic.

### 6. Pull requests

- Keep PRs focused. One logical change per PR.
- Write a clear description of what changed and why.
- Link the related issue if there is one.
- All CI checks must pass before merge.

## Repository structure

| Path | Description |
|---|---|
| `cmd/` | Go binary entrypoints (sandboxd, sbx, sandbox-agent) |
| `internal/` | Daemon-only Go packages |
| `agent/` | Guest agent Go packages |
| `protocol/` | Shared wire protocol types |
| `sdk/go/` | Go client SDK |
| `sdk/typescript/` | TypeScript client SDK |
| `sdk/python/` | Python client SDK |
| `mcp/` | MCP server |
| `images/` | Dockerfiles |
| `deploy/` | docker-compose, systemd units |
| `config/` | Example configuration files |

## Reporting bugs

Open an issue with:

1. What you expected to happen.
2. What actually happened.
3. Steps to reproduce.
4. Your environment (OS, Docker version, Go version).

## Code of conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold it.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
