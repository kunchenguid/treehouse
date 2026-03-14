#!/bin/sh
set -e

REPO="atinylittleshell/treehouse"
INSTALL_DIR="/usr/local/bin"

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

VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')"
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
  mv "${TMPDIR}/treehouse" "${INSTALL_DIR}/treehouse"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMPDIR}/treehouse" "${INSTALL_DIR}/treehouse"
fi

echo "treehouse ${VERSION} installed to ${INSTALL_DIR}/treehouse"
