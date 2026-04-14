#!/usr/bin/env bash
# Wait for sandboxd to become healthy with warm containers ready.
# Usage: ./scripts/wait-for-healthy.sh [timeout_seconds]

set -euo pipefail

TIMEOUT="${1:-60}"
URL="http://localhost:7522/health"
ELAPSED=0

echo "Waiting for sandboxd at ${URL} (timeout: ${TIMEOUT}s)..."

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  HEALTH=$(curl -sf "$URL" 2>/dev/null || echo "")
  if [ -n "$HEALTH" ]; then
    # Check that at least one pool profile has ready > 0.
    READY=$(echo "$HEALTH" | grep -o '"ready":[0-9]*' | head -1 | grep -o '[0-9]*' || echo "0")
    if [ "$READY" -gt 0 ]; then
      echo "sandboxd is healthy with ${READY} warm container(s) after ${ELAPSED}s."
      exit 0
    fi
    # sandboxd is up but pool not warm yet.
    if [ $((ELAPSED % 10)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
      echo "  sandboxd responding but pool not ready yet (${ELAPSED}s)..."
    fi
  fi
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

echo "ERROR: sandboxd did not become healthy with warm containers within ${TIMEOUT}s."
echo "Last health response: ${HEALTH:-<none>}"
exit 1
