.PHONY: all build build-sandboxd build-sbx build-agent test test-go test-ts test-py clean \
       test-integration test-integration-up test-integration-down test-all

all: build

build: build-sandboxd build-sbx build-agent

build-sandboxd:
	go build -o bin/sandboxd ./cmd/sandboxd

build-sbx:
	go build -o bin/sbx ./cmd/sbx

build-agent:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/sandbox-agent ./cmd/sandbox-agent

test: test-go test-ts test-py

test-go:
	go test ./...

test-ts:
	cd sdk/typescript && bun install && bunx --bun vitest run

test-py:
	@if [ ! -d sdk/python/.venv ]; then \
		python3 -m venv sdk/python/.venv && \
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

test-integration-up:
	docker compose -f $(COMPOSE_FILE) up --build -d
	./scripts/wait-for-healthy.sh 60

test-integration-down:
	docker compose -f $(COMPOSE_FILE) down -v --remove-orphans

test-integration: test-integration-up
	@status=0; \
	echo "=== Go integration tests ==="; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) go test ./sdk/go/integration/ -v -count=1 || status=1; \
	echo "=== TypeScript integration tests ==="; \
	(cd $(CURDIR)/sdk/typescript && SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) bunx --bun vitest run src/__integration__/ --reporter=verbose) || status=1; \
	echo "=== Python integration tests ==="; \
	if [ ! -d $(CURDIR)/sdk/python/.venv ]; then \
		python3 -m venv $(CURDIR)/sdk/python/.venv && \
		$(CURDIR)/sdk/python/.venv/bin/pip install -e "$(CURDIR)/sdk/python[dev]" --quiet; \
	fi; \
	SANDBOXD_ENDPOINT=$(SANDBOXD_ENDPOINT) $(CURDIR)/sdk/python/.venv/bin/python -m pytest $(CURDIR)/sdk/python/tests/integration/ -v || status=1; \
	cd $(CURDIR) && $(MAKE) test-integration-down; \
	exit $$status

test-all: test test-integration
