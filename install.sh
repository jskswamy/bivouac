#!/bin/sh
set -e

# bivouac installer
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sh
#   curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | sudo INSTALL_DIR=/usr/local/bin sh
#   curl -sSfL https://raw.githubusercontent.com/jskswamy/bivouac/main/install.sh | VERSION=v0.1.0 sh

REPO="jskswamy/bivouac"
BINARY="bivouac"
INSTALL_DIR="${INSTALL_DIR:-./bin}"

log() { echo "bivouac-installer: $*"; }
fail() { echo "bivouac-installer: ERROR: $*" >&2; exit 1; }

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    darwin|linux) ;;
    *) fail "unsupported OS: $OS (supported: macOS, Linux)" ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) fail "unsupported architecture: $ARCH" ;;
esac

if [ -z "$VERSION" ]; then
    log "fetching latest version..."
    VERSION=$(curl -sSfL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        fail "could not determine latest version"
    fi
fi
log "version: $VERSION"

ARCHIVE="bivouac_${VERSION#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/sha256sums.txt"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

log "downloading $ARCHIVE..."
curl -sSfL -o "${TMPDIR}/${ARCHIVE}" "$URL" || fail "download failed: $URL"

log "verifying checksum..."
curl -sSfL -o "${TMPDIR}/sha256sums.txt" "$CHECKSUM_URL" || fail "checksum download failed"
EXPECTED=$(grep "$ARCHIVE" "${TMPDIR}/sha256sums.txt" | awk '{print $1}')
if [ -z "$EXPECTED" ]; then
    fail "archive not found in sha256sums.txt"
fi
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
else
    ACTUAL=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
fi
if [ "$EXPECTED" != "$ACTUAL" ]; then
    fail "checksum mismatch: expected $EXPECTED, got $ACTUAL"
fi
log "checksum verified"

mkdir -p "$INSTALL_DIR"
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"
cp "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
chmod +x "${INSTALL_DIR}/${BINARY}"

log "installed $BINARY $VERSION to ${INSTALL_DIR}/${BINARY}"

case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *) log "NOTE: add ${INSTALL_DIR} to your PATH" ;;
esac
