#!/bin/sh
set -e

# Install script for sandboxd and sbx.
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/byggflow/sandbox/main/scripts/install.sh | sh -s -- --version v0.1.0

REPO="byggflow/sandbox"
INSTALL_DIR="/usr/local/bin"
VERSION=""

usage() {
  cat <<EOF
Install sandboxd and sbx binaries.

Usage:
  install.sh [OPTIONS]

Options:
  --version VERSION   Install a specific version (e.g. v0.1.0). Default: latest.
  --dir DIR           Install directory. Default: /usr/local/bin
  -h, --help          Show this help message.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --dir)     INSTALL_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *)         echo "Unknown option: $1"; usage; exit 1 ;;
  esac
done

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    *)       echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *)              echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ -z "$VERSION" ]; then
  echo "Fetching latest version..."
  VERSION="$(latest_version)"
fi

if [ -z "$VERSION" ]; then
  echo "Error: could not determine latest version." >&2
  echo "Specify one with --version." >&2
  exit 1
fi

TARBALL="sandbox-${VERSION}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${TARBALL}..."
curl -fsSL "$URL" -o "${TMPDIR}/${TARBALL}"

echo "Extracting to ${INSTALL_DIR}..."
tar xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

# Install with appropriate permissions
if [ -w "$INSTALL_DIR" ]; then
  cp "${TMPDIR}/sandboxd" "${TMPDIR}/sbx" "$INSTALL_DIR/"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo cp "${TMPDIR}/sandboxd" "${TMPDIR}/sbx" "$INSTALL_DIR/"
fi

if [ -w "$INSTALL_DIR" ]; then
  chmod +x "${INSTALL_DIR}/sandboxd" "${INSTALL_DIR}/sbx"
else
  sudo chmod +x "${INSTALL_DIR}/sandboxd" "${INSTALL_DIR}/sbx"
fi

echo ""
echo "Installed:"
echo "  sandboxd  ${VERSION}  ${INSTALL_DIR}/sandboxd"
echo "  sbx       ${VERSION}  ${INSTALL_DIR}/sbx"
echo ""
echo "Run 'sandboxd --help' or 'sbx --help' to get started."
