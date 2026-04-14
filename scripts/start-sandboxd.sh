#!/usr/bin/env bash
# Start sandboxd natively for integration testing.
# Usage: ./scripts/start-sandboxd.sh [config_file]
#
# Creates required directories and starts sandboxd in the background.
# Writes the PID to /tmp/sandboxd-test.pid for later cleanup.

set -euo pipefail

CONFIG="${1:-config/sandboxd.test.toml}"
PIDFILE="/tmp/sandboxd-test.pid"

if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "sandboxd already running (PID $(cat "$PIDFILE"))"
  exit 0
fi

# Ensure required directories exist.
sudo mkdir -p /var/run/sandboxd /var/lib/sandboxd
sudo chmod 755 /var/run/sandboxd /var/lib/sandboxd

echo "Starting sandboxd with config: ${CONFIG}"
sudo ./bin/sandboxd --config "$CONFIG" &
SANDBOXD_PID=$!
echo "$SANDBOXD_PID" | sudo tee "$PIDFILE" > /dev/null

echo "sandboxd started (PID ${SANDBOXD_PID})"
