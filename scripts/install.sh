#!/usr/bin/env bash
# pgcli install script — installs the pg binary and creates default config.
# Supports Linux and macOS (amd64/arm64).
set -euo pipefail

REPO="mars-base/pgcli"
BINARY="pg"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[0;33m%s\033[0m\n' "$*"; }

die() { red "ERROR: $*"; exit 1; }

detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       die "Unsupported OS: $(uname -s). Only Linux and macOS are supported." ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        arm64|aarch64) echo "arm64" ;;
        *)             die "Unsupported architecture: $(uname -m)" ;;
    esac
}

latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/'
}

install_binary() {
    local os="$1" arch="$2" version="$3"
    local tarball="${REPO##*/}_${version#v}_${os}_${arch}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${version}/${tarball}"

    yellow "-> Downloading $BINARY $version ($os/$arch)..."
    local tmp
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    if ! curl -fsSL -o "$tmp/$tarball" "$url"; then
        die "Download failed: $url"
    fi

    tar -xzf "$tmp/$tarball" -C "$tmp"
    mkdir -p "$INSTALL_DIR"
    mv "$tmp/$BINARY" "$INSTALL_DIR/$BINARY"
    chmod +x "$INSTALL_DIR/$BINARY"
    green "  [OK] $BINARY installed to $INSTALL_DIR/$BINARY"
}

install_podman_launcher() {
    if [ "$(uname -s)" != "Linux" ]; then
        return
    fi
    if command -v podman &>/dev/null; then
        green "  [OK] podman already available"
        return
    fi
    yellow "-> Installing podman-launcher..."
    mkdir -p "$HOME/.local/bin"
    curl -fsSL -o "$HOME/.local/bin/podman" \
        "https://github.com/89luca89/podman-launcher/releases/latest/download/podman-launcher-amd64"
    chmod +x "$HOME/.local/bin/podman"
    green "  [OK] podman-launcher installed"
}

check_path() {
    if ! command -v "$BINARY" &>/dev/null && [ ! -x "$INSTALL_DIR/$BINARY" ]; then
        yellow ""
        yellow "  NOTE: Add $INSTALL_DIR to your PATH:"
        yellow "    export PATH=\"$INSTALL_DIR:\$PATH\""
        yellow ""
    fi
}

main() {
    echo "=== pgcli installer ==="
    echo ""

    local os arch version
    os="$(detect_os)"
    arch="$(detect_arch)"

    # Version from argument or latest release
    if [ $# -ge 1 ]; then
        version="$1"
    else
        version="$(latest_version)"
        if [ -z "$version" ]; then
            die "Could not determine latest version. Usage: install.sh [VERSION]"
        fi
    fi
    echo "  Version: $version"
    echo "  OS/Arch: $os/$arch"
    echo "  Install: $INSTALL_DIR"
    echo ""

    install_binary "$os" "$arch" "$version"
    install_podman_launcher
    check_path

    echo ""
    green "Installation complete!"
    echo ""
    echo "  Quick start:"
    echo "    pg config init --add default --base-dir /data/pg"
    echo "    pg start"
    echo "    pg status"
    echo ""
}

main "$@"
