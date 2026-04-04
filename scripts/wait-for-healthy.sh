#!/usr/bin/env bash
# Wait for sandboxd to become healthy on localhost:7522.
# Usage: ./scripts/wait-for-healthy.sh [timeout_seconds]

set -euo pipefail

TIMEOUT="${1:-30}"
URL="http://localhost:7522/health"
ELAPSED=0

echo "Waiting for sandboxd at ${URL} (timeout: ${TIMEOUT}s)..."

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  if curl -sf "$URL" > /dev/null 2>&1; then
    echo "sandboxd is healthy after ${ELAPSED}s."
    exit 0
  fi
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

echo "ERROR: sandboxd did not become healthy within ${TIMEOUT}s."
exit 1
