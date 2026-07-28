#!/bin/sh
# Codebridge
# SPDX-License-Identifier: AGPL-3.0-or-later

set -eu

REPOSITORY="qunv/codebridge"
DEFAULT_VERSION="v1.0.1"
VERSION="${CODEBRIDGE_VERSION:-$DEFAULT_VERSION}"
INSTALL_DIR="${CODEBRIDGE_INSTALL_DIR:-$HOME/.local/bin}"
DOWNLOAD_BASE_URL="${CODEBRIDGE_DOWNLOAD_BASE_URL:-}"

usage() {
    cat <<'EOF'
Install Codebridge from a GitHub release.

Usage:
  install.sh [--version VERSION] [--install-dir DIRECTORY]

Options:
  --version VERSION       Release tag to install. Default: v1.0.1
  --install-dir DIRECTORY Installation directory. Default: ~/.local/bin
  -h, --help              Show this help message

Environment variables:
  CODEBRIDGE_VERSION            Release tag to install
  CODEBRIDGE_INSTALL_DIR        Installation directory
  CODEBRIDGE_DOWNLOAD_BASE_URL  Override the release asset base URL

Download behavior:
  Uses an authenticated GitHub CLI session when available, which is required
  for private repositories. Otherwise, downloads release assets with curl.
EOF
}

fail() {
    printf 'codebridge installer: %s\n' "$1" >&2
    exit 1
}

need_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
        return
    fi

    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
        return
    fi

    fail "sha256sum or shasum is required to verify the download"
}

github_cli_authenticated() {
    command -v gh >/dev/null 2>&1 && gh auth token >/dev/null 2>&1
}

download_asset() {
    asset="$1"
    if [ "$USE_GH" = "true" ]; then
        gh release download "$VERSION" \
            --repo "$REPOSITORY" \
            --pattern "$asset" \
            --dir "$TMP_DIR" \
            --clobber >/dev/null
        return
    fi
    curl -fsSL "${DOWNLOAD_BASE_URL}/${asset}" -o "$TMP_DIR/$asset" ||
        fail "download failed; authenticate GitHub CLI for a private repository or set CODEBRIDGE_DOWNLOAD_BASE_URL"
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || fail "--version requires a value"
            VERSION="$2"
            shift 2
            ;;
        --install-dir)
            [ "$#" -ge 2 ] || fail "--install-dir requires a value"
            INSTALL_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

case "$VERSION" in
    v*) ;;
    *) VERSION="v$VERSION" ;;
esac

need_command tar
need_command awk
need_command mktemp
need_command uname

case "$(uname -s)" in
    Linux) OS="linux" ;;
    Darwin) OS="darwin" ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
esac

RELEASE_VERSION="${VERSION#v}"
ARCHIVE="codebridge_${RELEASE_VERSION}_${OS}_${ARCH}.tar.gz"

USE_GH="false"
if [ -z "$DOWNLOAD_BASE_URL" ] && github_cli_authenticated; then
    USE_GH="true"
else
    need_command curl
    if [ -z "$DOWNLOAD_BASE_URL" ]; then
        DOWNLOAD_BASE_URL="https://github.com/${REPOSITORY}/releases/download/${VERSION}"
    fi
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

printf 'Downloading Codebridge %s for %s/%s...\n' "$VERSION" "$OS" "$ARCH"
download_asset "$ARCHIVE"
download_asset "checksums.txt"

EXPECTED_CHECKSUM="$(awk -v archive="$ARCHIVE" '$2 == archive { print $1; exit }' "$TMP_DIR/checksums.txt")"
[ -n "$EXPECTED_CHECKSUM" ] || fail "checksum entry not found for $ARCHIVE"

ACTUAL_CHECKSUM="$(sha256_file "$TMP_DIR/$ARCHIVE")"
[ "$ACTUAL_CHECKSUM" = "$EXPECTED_CHECKSUM" ] || fail "checksum verification failed for $ARCHIVE"

printf 'Checksum verified.\n'
tar -xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR"
[ -f "$TMP_DIR/codebridge" ] || fail "release archive does not contain codebridge"

mkdir -p "$INSTALL_DIR"
cp "$TMP_DIR/codebridge" "$INSTALL_DIR/codebridge"
chmod 755 "$INSTALL_DIR/codebridge"

printf 'Installed Codebridge to %s/codebridge\n' "$INSTALL_DIR"
"$INSTALL_DIR/codebridge" --version

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        printf '\nAdd Codebridge to PATH by placing this line in your shell profile:\n'
        printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
        ;;
esac
