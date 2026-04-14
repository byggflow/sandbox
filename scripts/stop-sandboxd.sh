#!/usr/bin/env bash
# Stop sandboxd started by start-sandboxd.sh.
# Also cleans up Docker containers and network created during testing.

set -euo pipefail

PIDFILE="/tmp/sandboxd-test.pid"

if [ -f "$PIDFILE" ]; then
  PID=$(cat "$PIDFILE")
  if kill -0 "$PID" 2>/dev/null; then
    echo "Stopping sandboxd (PID ${PID})..."
    sudo kill "$PID" 2>/dev/null || true
    # Wait up to 5s for graceful shutdown.
    for i in $(seq 1 5); do
      kill -0 "$PID" 2>/dev/null || break
      sleep 1
    done
    # Force kill if still running.
    kill -0 "$PID" 2>/dev/null && sudo kill -9 "$PID" 2>/dev/null || true
  fi
  sudo rm -f "$PIDFILE"
fi

# Clean up any leftover sandbox containers.
CONTAINERS=$(docker ps -a --filter "label=sandboxd=true" -q 2>/dev/null || true)
if [ -n "$CONTAINERS" ]; then
  echo "Cleaning up sandbox containers..."
  docker rm -f $CONTAINERS 2>/dev/null || true
fi

# Remove test network if it exists.
docker network rm sandboxd-test-net 2>/dev/null || true

echo "Cleanup complete."
