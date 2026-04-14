.PHONY: all build build-sandboxd build-sbx build-agent build-ts test test-go test-ts test-mcp test-py clean \
       build-linux test-integration test-integration-up test-integration-down \
       test-integration-native test-integration-native-up test-integration-native-down test-all

all: build

build: build-sandboxd build-sbx build-agent build-ts

build-sandboxd:
	go build -o bin/sandboxd ./cmd/sandboxd

build-sbx:
	go build -o bin/sbx ./cmd/sbx

build-agent:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/sandbox-agent ./cmd/sandbox-agent

build-ts:
	cd sdk/typescript && bun install && bun run build
	cd mcp && bun install && bun run build

# Cross-compile Go binaries for Linux (used by Docker images in test/CI).
# Detects host architecture and builds for linux/<arch>.
GOARCH := $(shell go env GOARCH)
build-linux:
	@mkdir -p bin/linux/$(GOARCH)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags="-s -w" -o bin/linux/$(GOARCH)/sandbox-agent ./cmd/sandbox-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags="-s -w" -o bin/linux/$(GOARCH)/sandboxd ./cmd/sandboxd

test: test-go test-ts test-mcp test-py

test-go:
	go test ./...

test-ts:
	cd sdk/typescript && bun install && bun run build && bunx --bun vitest run

test-mcp:
	cd mcp && bun install && bunx --bun vitest run

test-py:
	@if [ ! -d sdk/python/.venv ]; then \
		python3 -m venv sdk/python/.venv && \
		sdk/python/.venv/bin/pip install --upgrade pip --quiet && \
		sdk/python/.venv/bin/pip install -e "sdk/python[dev]" --quiet; \
	fi
	sdk/python/.venv/bin/python -m pytest sdk/python/tests/ -v

clean:
	rm -rf bin/

# ---------------------------------------------------------------------------
# Integration tests (require Docker)
# ---------------------------------------------------------------------------

COMPOSE_FILE := docker-compose.test.yml
SANDBOXD_ENDPOINT := http://localhost:7522

# --- Docker-compose mode (default, works on macOS and Linux) ---
# Runs sandboxd in a container with Docker socket mounted.
# The ICC isolation test is skipped because compose requires ICC=true for
# sandboxd-to-agent communication. Native mode (below) tests ICC properly.

test-integration-up: build-linux
	TARGETARCH=$(GOARCH) docker compose -f $(COMPOSE_FILE) up --build -d
	./scripts/wait-for-healthy.sh 90

test-integration-down:
	docker compose -f $(COMPOSE_FILE) down -v --remove-orphans

test-integration: test-integration-up
	@status=0; \
	echo "=== Go integration tests ==="; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) go test ./sdk/go/integration/ -v -count=1 -timeout=120s || status=1; \
	echo "=== TypeScript integration tests ==="; \
	(cd $(CURDIR)/sdk/typescript && SANDBOXD_SKIP_ICC_TEST=1 bun install && SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) SANDBOXD_SKIP_ICC_TEST=1 bunx --bun vitest run -c vitest.integration.config.ts --reporter=verbose) || status=1; \
	echo "=== Python integration tests ==="; \
	if [ ! -d $(CURDIR)/sdk/python/.venv ]; then \
		python3 -m venv $(CURDIR)/sdk/python/.venv && \
		$(CURDIR)/sdk/python/.venv/bin/pip install -e "$(CURDIR)/sdk/python[dev]" --quiet; \
	fi; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) $(CURDIR)/sdk/python/.venv/bin/python -m pytest $(CURDIR)/sdk/python/tests/integration/ -v || status=1; \
	cd $(CURDIR) && $(MAKE) test-integration-down; \
	exit $$status

# --- Native mode (Linux CI — sandboxd runs directly on the host) ---
# All tests run including port tunneling and ICC isolation.

test-integration-native-up: build-sandboxd build-agent build-linux
	@# Build the base sandbox image (agent binary must be cross-compiled for linux).
	docker build -t sandbox-test-base:latest --build-arg TARGETARCH=$(GOARCH) images/base/
	./scripts/start-sandboxd.sh config/sandboxd.test.toml
	./scripts/wait-for-healthy.sh 90

test-integration-native-down:
	./scripts/stop-sandboxd.sh

test-integration-native: test-integration-native-up
	@status=0; \
	echo "=== Go integration tests (all) ==="; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) go test ./sdk/go/integration/ -v -count=1 -timeout=120s || status=1; \
	echo "=== TypeScript integration tests ==="; \
	(cd $(CURDIR)/sdk/typescript && bun install && SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) bunx --bun vitest run -c vitest.integration.config.ts --reporter=verbose) || status=1; \
	echo "=== Python integration tests ==="; \
	if [ ! -d $(CURDIR)/sdk/python/.venv ]; then \
		python3 -m venv $(CURDIR)/sdk/python/.venv && \
		$(CURDIR)/sdk/python/.venv/bin/pip install -e "$(CURDIR)/sdk/python[dev]" --quiet; \
	fi; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) $(CURDIR)/sdk/python/.venv/bin/python -m pytest $(CURDIR)/sdk/python/tests/integration/ -v || status=1; \
	cd $(CURDIR) && $(MAKE) test-integration-native-down; \
	exit $$status

test-all: test test-integration
