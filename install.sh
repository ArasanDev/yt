#!/bin/sh
# Install a prebuilt yt release on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/ArasanDev/yt/main/install.sh | sh
#
# Environment:
#   YT_VERSION      release tag to install (default: latest, e.g. v0.1.0)
#   YT_INSTALL_DIR  destination directory (default: /usr/local/bin if writable,
#                   otherwise ~/.local/bin)
set -eu

REPO="ArasanDev/yt"
BIN="yt"
VERSION="${YT_VERSION:-latest}"
INSTALL_DIR="${YT_INSTALL_DIR:-}"

say() { printf '%s\n' "$*"; }
fail() { printf 'yt install: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
Install yt from GitHub Releases.

Usage:
  curl -fsSL https://raw.githubusercontent.com/ArasanDev/yt/main/install.sh | sh

Environment:
  YT_VERSION      release tag to install (default: latest, e.g. v0.1.0)
  YT_INSTALL_DIR  destination directory (default: /usr/local/bin if writable,
                  otherwise ~/.local/bin)
EOF
}

case "${1:-}" in
	-h|--help) usage; exit 0 ;;
esac

command -v uname >/dev/null 2>&1 || fail "uname is required"

if command -v curl >/dev/null 2>&1; then
	DOWNLOADER=curl
elif command -v wget >/dev/null 2>&1; then
	DOWNLOADER=wget
else
	fail "curl or wget is required"
fi

download() {
	# download URL DEST
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsSL "$1" -o "$2"
	else
		wget -qO "$2" "$1"
	fi
}

download_stdout() {
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsSL "$1"
	else
		wget -qO- "$1"
	fi
}

case "$(uname -s)" in
	Darwin) OS=Darwin ;;
	Linux) OS=Linux ;;
	*) fail "unsupported operating system: $(uname -s); use 'go install' or a release binary" ;;
esac

case "$(uname -m)" in
	x86_64|amd64) ARCH=x86_64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$VERSION" = "latest" ]; then
	VERSION="$(download_stdout "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
	[ -n "$VERSION" ] || fail "could not determine the latest release; set YT_VERSION"
fi

VERSION_NO_V="${VERSION#v}"
ASSET="${BIN}_${VERSION_NO_V}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

TMP_BASE="${TMPDIR:-/tmp}"
TMP_DIR="$(mktemp -d "$TMP_BASE/yt-install.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

say "Downloading $ASSET ($VERSION)..."
download "$BASE/$ASSET" "$TMP_DIR/$ASSET" || fail "download failed: $BASE/$ASSET"
download "$BASE/checksums.txt" "$TMP_DIR/checksums.txt" || fail "could not download checksums.txt"

EXPECTED="$(awk -v name="$ASSET" '$2 == name { print $1 }' "$TMP_DIR/checksums.txt")"
[ -n "$EXPECTED" ] || fail "checksums.txt does not list $ASSET"
if command -v sha256sum >/dev/null 2>&1; then
	ACTUAL="$(sha256sum "$TMP_DIR/$ASSET" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
	ACTUAL="$(shasum -a 256 "$TMP_DIR/$ASSET" | awk '{ print $1 }')"
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$EXPECTED" = "$ACTUAL" ] || fail "checksum mismatch for $ASSET"

tar -xzf "$TMP_DIR/$ASSET" -C "$TMP_DIR" "$BIN" || fail "could not extract $ASSET"

if [ -z "$INSTALL_DIR" ]; then
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		INSTALL_DIR=/usr/local/bin
	else
		INSTALL_DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$INSTALL_DIR" || fail "could not create $INSTALL_DIR"

# Replace the directory entry instead of writing over a mapped executable:
# overwriting a running binary in place makes macOS SIGKILL the next exec.
STAGED="$INSTALL_DIR/.$BIN.new.$$"
cp "$TMP_DIR/$BIN" "$STAGED"
chmod 0755 "$STAGED"
mv -f "$STAGED" "$INSTALL_DIR/$BIN"

say "Installed $("$INSTALL_DIR/$BIN" --version 2>/dev/null || echo "$BIN $VERSION") to $INSTALL_DIR/$BIN"
case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) say "Add $INSTALL_DIR to PATH to run 'yt' from anywhere." ;;
esac
