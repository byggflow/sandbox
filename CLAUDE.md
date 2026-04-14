## Commit Message Conventions
This repository follows the Conventional Commits specification for commit messages. This means that each commit message should be structured in the following format:

```<type>[optional scope]: <description>
[optional body]
[optional footer(s)]
```
Where:
- `<type>` is a required field that indicates the type of change being made. Common types include `feat` for new features, `fix` for bug fixes, `docs` for documentation changes, `style` for code formatting, `refactor` for code changes that neither fix a bug nor add a feature, and `test` for adding or modifying tests.
- `[optional scope]` is an optional field that provides additional context about the change, such as the area of the codebase affected (e.g., `api`, `ui`, `database`).
- `<description>` is a required field that provides a brief summary of the change.
- `[optional body]` is an optional field that can include a more detailed description of the change, including the motivation for the change and any relevant background information.
- `[optional footer(s)]` is an optional field that can include any additional information, such as breaking changes or issues closed by the commit.

Examples of valid commit messages:

```feat(api): add new endpoint for user authentication
fix(ui): resolve issue with button alignment
docs: update README with installation instructions
style: reformat code using Prettier
refactor: simplify data fetching logic
test: add unit tests for user model
```
By following these conventions, we can maintain a clear and consistent commit history that makes it easier to understand the changes being made and the reasons behind them. This also helps with generating changelogs and automating releases based on commit messages.

Please do not co-author commits with AI assistants, as this can create confusion about the source of the changes and may not accurately reflect the contributions of human developers. Instead, focus on writing clear and descriptive commit messages that accurately convey the intent and impact of the changes being made.

## Conventions

See `conventions/` for the full conventions with examples in both TypeScript and Go:

- **`conventions/QUALITY.md`** -API design: verb+noun entry points, category objects, single call backbone, no global state, fail-early errors.
- **`conventions/PERFORMANCE.md`** -Performance: data structure selection, bounded collections, early exits, signal over polling, hot-path allocations, batching, coordination.

## Documentation

When making code changes, always check and update related documentation. Key docs:

- `README.md` -- top-level feature descriptions, security section, API reference
- `sdk/typescript/README.md` -- TypeScript SDK usage, features, examples
- `SECURITY.md` -- security design overview

Before finishing a PR, verify that any feature or behavior described in these files still matches the code. If a change adds, removes, or modifies a feature, update the relevant docs in the same PR.

## Repository structure

This is a monorepo with Go and TypeScript code:

- `cmd/` -Go binary entrypoints (`sandboxd`, `sbx`, `sandbox-agent`)
- `internal/` -daemon-only Go packages (not importable externally)
- `agent/` -guest agent Go packages
- `protocol/` -shared wire protocol types (used by both daemon and agent)
- `sdk/go/` -public Go client SDK
- `sdk/typescript/` -TypeScript client SDK (`@byggflow/sandbox`)
- `images/` -Dockerfiles for published images
- `deploy/` -docker-compose, systemd units
- `config/` -example configuration files

## Go

Go is the primary language for the daemon, agent, CLI, and Go SDK.

- Use standard library where possible. Minimize dependencies.
- Use `context.Context` as the first parameter for any function that does I/O.
- Use `internal/` for packages that should not be imported outside this module.
- Errors are values -return `error`, don't panic. Wrap errors with `fmt.Errorf("doing x: %w", err)`.
- Use `go test ./...` to run all Go tests. Use the standard `testing` package.
- Build binaries with `make build` or `go build ./cmd/<name>`.

## TypeScript (sdk/typescript/)

Prefer Bun as the local dev runtime and package manager for speed, but **published libraries must work on Node, Bun, Deno, and browsers**.

- Use `bun install`, `bun run <script>`, and `bunx` for local development.
- In library/SDK source code, use `node:` prefixed imports (e.g. `node:fs`, `node:crypto`) instead of Bun-specific APIs. This ensures cross-runtime compatibility.
- Bun-specific APIs (`Bun.serve`, `Bun.file`, `bun:sqlite`, etc.) are fine in internal tooling and scripts that only run locally, but never in published packages.

### Testing

Use `vitest` for testing TypeScript. Don't use `bun:test` or `jest`.

```ts#example.test.ts
import { test, expect } from "vitest";

test("hello world", () => {
  expect(1).toBe(1);
});
```

Run tests with `bunx --bun vitest run` or `bun run test` (if a test script is defined). Always use the `--bun` flag with vitest to run under Bun's runtime instead of Node.

