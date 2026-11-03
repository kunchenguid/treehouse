#!/bin/sh
set -e

REPO="kunchenguid/treehouse"

# Prefer ~/.local/bin if it exists and is in PATH (no sudo needed).
# Fall back to /usr/local/bin otherwise.
if echo "$PATH" | tr ':' '\n' | grep -qx "$HOME/.local/bin"; then
  INSTALL_DIR="$HOME/.local/bin"
else
  INSTALL_DIR="/usr/local/bin"
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Resolve the latest tag without the unauthenticated REST API's rate limit.
latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
VERSION=""
case "$latest_url" in
  */releases/tag/*)
    VERSION="${latest_url##*/releases/tag/}"
    VERSION="${VERSION%%\?*}"
    VERSION="${VERSION%%#*}"
    VERSION="${VERSION%/}"
    ;;
esac
if [ -z "$VERSION" ]; then
  echo "Failed to determine latest version"
  exit 1
fi

VERSION_NUM="${VERSION#v}"
FILENAME="treehouse-v${VERSION_NUM}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading treehouse ${VERSION} for ${OS}/${ARCH}..."
curl -fsSL "$URL" -o "${TMPDIR}/${FILENAME}"
tar xzf "${TMPDIR}/${FILENAME}" -C "$TMPDIR"

if [ -w "$INSTALL_DIR" ]; then
  mkdir -p "$INSTALL_DIR"
  mv "${TMPDIR}/treehouse" "${INSTALL_DIR}/treehouse"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mkdir -p "$INSTALL_DIR"
  sudo mv "${TMPDIR}/treehouse" "${INSTALL_DIR}/treehouse"
fi

echo "treehouse ${VERSION} installed to ${INSTALL_DIR}/treehouse"
