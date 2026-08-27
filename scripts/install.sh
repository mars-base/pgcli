#!/usr/bin/env bash
# pgcli install script — installs the pg binary and container dependencies.
# Supports Linux and macOS (amd64/arm64).
set -euo pipefail

REPO="mars-base/pgcli"
BINARY="pg"
INSTALL_DIR="${INSTALL_DIR:-}"

# Container images (pulled during install for faster first startup)
PG_IMAGE="ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0"
BACKUP_IMAGE="ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0"

red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[0;33m%s\033[0m\n' "$*"; }
dim()   { printf '\033[0;2m%s\033[0m\n' "$*"; }

die() { red "ERROR: $*"; exit 1; }

IS_MACOS=false
IS_LINUX=false
case "$(uname -s)" in
    Darwin) IS_MACOS=true ;;
    Linux)  IS_LINUX=true ;;
esac

# Determine default install directory if not set via environment
if [ -z "$INSTALL_DIR" ]; then
    if [ -w /usr/local/bin ] 2>/dev/null; then
        INSTALL_DIR="/usr/local/bin"
    elif $IS_MACOS; then
        INSTALL_DIR="/usr/local/bin"
    elif command -v sudo &>/dev/null && sudo -n true 2>/dev/null; then
        INSTALL_DIR="/usr/local/bin"
    else
        INSTALL_DIR="$HOME/.local/bin"
    fi
fi

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
    trap "rm -rf '$tmp'" EXIT

    if ! curl -fsSL -o "$tmp/$tarball" "$url"; then
        die "Download failed: $url"
    fi

    tar -xzf "$tmp/$tarball" -C "$tmp"
    if [ -w "$INSTALL_DIR" ]; then
        mkdir -p "$INSTALL_DIR"
        mv "$tmp/$BINARY" "$INSTALL_DIR/$BINARY"
        chmod +x "$INSTALL_DIR/$BINARY"
    else
        as_root mkdir -p "$INSTALL_DIR"
        as_root mv "$tmp/$BINARY" "$INSTALL_DIR/$BINARY"
        as_root chmod +x "$INSTALL_DIR/$BINARY"
    fi
    green "  [OK] $BINARY installed to $INSTALL_DIR/$BINARY"
}

# ─── Linux: distro detection ─────────────────────────────────────────

detect_linux_distro() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        case "${ID:-}" in
            ubuntu|linuxmint|pop|kali|raspbian|debian) echo "debian"; return 0 ;;
            fedora)                                    echo "fedora"; return 0 ;;
            rhel|centos|rocky|almalinux|ol|cloudlinux) echo "rhel";   return 0 ;;
            arch|manjaro|garuda|endeavouros|cachyos)   echo "arch";   return 0 ;;
            alpine)                                    echo "alpine"; return 0 ;;
            opensuse*|sles|sle-micro|suse)             echo "suse";   return 0 ;;
        esac
        for _like in ${ID_LIKE:-}; do
            case "$_like" in
                debian|ubuntu)      echo "debian"; return 0 ;;
                rhel|fedora|centos) echo "rhel";   return 0 ;;
                arch)               echo "arch";   return 0 ;;
                suse|opensuse)      echo "suse";   return 0 ;;
                alpine)             echo "alpine"; return 0 ;;
            esac
        done
    fi
    for _pm in apt-get dnf yum zypper pacman apk; do
        if command -v "$_pm" >/dev/null 2>&1; then
            case "$_pm" in
                apt-get) echo "debian" ;;
                dnf|yum) echo "rhel" ;;
                zypper)  echo "suse" ;;
                pacman)  echo "arch" ;;
                apk)     echo "alpine" ;;
            esac
            return 0
        fi
    done
    echo "unknown"
}

detect_pkg_manager() {
    local distro
    distro="$(detect_linux_distro)"
    case "$distro" in
        debian)  command -v apt-get >/dev/null 2>&1 && { echo apt-get; return 0; } ;;
        fedora|rhel)
            command -v dnf >/dev/null 2>&1 && { echo dnf; return 0; }
            command -v yum >/dev/null 2>&1 && { echo yum; return 0; } ;;
        arch)    command -v pacman >/dev/null 2>&1 && { echo pacman; return 0; } ;;
        alpine)  command -v apk >/dev/null 2>&1 && { echo apk; return 0; } ;;
        suse)    command -v zypper >/dev/null 2>&1 && { echo zypper; return 0; } ;;
    esac
    echo ""
}

as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

install_pkgs() {
    local pm
    pm="$(detect_pkg_manager)"
    [ -z "$pm" ] && return 1
    case "$pm" in
        apt-get) as_root apt-get update && as_root apt-get install -y "$@" ;;
        dnf)     as_root dnf install -y "$@" ;;
        yum)     as_root yum install -y "$@" ;;
        zypper)  as_root zypper install -y "$@" ;;
        pacman)  as_root pacman -S --noconfirm --needed "$@" ;;
        apk)     as_root apk add --no-cache "$@" ;;
        *)       return 1 ;;
    esac
}

# ─── Linux dependencies ──────────────────────────────────────────────

setup_linux_deps() {
    yellow "-> Checking Linux dependencies..."

    # uidmap (newuidmap/newgidmap) — required for rootless podman
    if command -v newuidmap >/dev/null 2>&1; then
        green "  [OK] newuidmap available (uidmap)"
    else
        yellow "  [!] newuidmap not found — installing uidmap..."
        if install_pkgs uidmap; then
            green "  [OK] uidmap installed"
        else
            yellow "  [!] Failed to install uidmap"
            dim "      Rootless podman requires newuidmap/newgidmap"
            dim "      Install the uidmap package for your distro, then re-run this script"
        fi
    fi

    # /etc/containers/policy.json — required for podman to pull images
    local policy_file="/etc/containers/policy.json"
    local policy_default='{"default":[{"type":"insecureAcceptAnything"}]}'
    if [ -f "$policy_file" ]; then
        green "  [OK] $policy_file exists"
    else
        yellow "  [!] $policy_file not found — creating..."
        if mkdir -p /etc/containers 2>/dev/null && printf '%s\n' "$policy_default" > "$policy_file" 2>/dev/null; then
            green "  [OK] Created $policy_file"
        elif sudo mkdir -p /etc/containers 2>/dev/null && printf '%s\n' "$policy_default" | sudo tee "$policy_file" >/dev/null 2>&1; then
            green "  [OK] Created $policy_file (via sudo)"
        else
            yellow "  [!] Could not create $policy_file (no write permission)"
            dim "      Run manually:"
            dim "      sudo mkdir -p /etc/containers"
            dim "      echo '{\"default\":[{\"type\":\"insecureAcceptAnything\"}]}' | sudo tee /etc/containers/policy.json"
        fi
    fi

    # User namespace — check if unprivileged userns is enabled
    if [ -f /proc/sys/kernel/unprivileged_userns_clone ]; then
        if [ "$(cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null)" = "1" ]; then
            green "  [OK] unprivileged user namespaces enabled"
        else
            yellow "  [!] unprivileged user namespaces disabled"
            dim "      Rootless podman requires user namespaces."
            dim "      Enable temporarily:"
            dim "        sudo sysctl -w kernel.unprivileged_userns_clone=1"
            dim "      Enable permanently:"
            dim "        echo 'kernel.unprivileged_userns_clone=1' | sudo tee /etc/sysctl.d/99-userns.conf"
        fi
    fi
}

# ─── macOS: podman machine ──────────────────────────────────────────

setup_macos_deps() {
    yellow "-> Checking macOS dependencies..."

    # Homebrew
    if ! command -v brew &>/dev/null; then
        yellow "  [!] Homebrew not found — install it first:"
        dim "      /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        return
    fi
    green "  [OK] Homebrew found"

    # Podman
    if ! command -v podman &>/dev/null; then
        yellow "  [!] podman not found — installing via Homebrew..."
        brew install podman
        green "  [OK] podman installed"
    else
        green "  [OK] podman available ($(podman --version 2>/dev/null | head -1))"
    fi

    # Podman machine
    if podman machine list >/dev/null 2>&1; then
        if podman machine list 2>/dev/null | grep -qi "currently running"; then
            green "  [OK] podman machine is running"
        else
            yellow "  [!] podman machine configured but not running — starting..."
            if podman machine start; then
                green "  [OK] podman machine started"
            else
                yellow "  [!] Failed to start podman machine. Run 'podman machine start' manually."
            fi
        fi
    else
        yellow "  [!] podman machine not initialized — creating VM..."
        dim "      This will download a VM image and may take a few minutes."
        if podman machine init --now; then
            green "  [OK] podman machine initialized and running"
        else
            yellow "  [!] Failed to initialize podman machine"
            dim "      Run 'podman machine init --now' manually"
        fi
    fi

    # Re-sign binary for macOS Gatekeeper
    codesign --force --sign - "$INSTALL_DIR/$BINARY" 2>/dev/null \
        && green "  [OK] $BINARY re-signed for macOS" \
        || dim "  [!] re-sign skipped (may need: codesign --force --sign - $INSTALL_DIR/$BINARY)"
}

# ─── Podman install (Linux) ──────────────────────────────────────────

install_podman_launcher() {
    if command -v podman &>/dev/null; then
        green "  [OK] podman already available"
        return
    fi

    yellow "-> Installing podman-launcher..."
    mkdir -p "$HOME/.local/bin"
    local arch
    arch="$(detect_arch)"
    curl -fsSL -o "$HOME/.local/bin/podman" \
        "https://github.com/89luca89/podman-launcher/releases/latest/download/podman-launcher-${arch}"
    chmod +x "$HOME/.local/bin/podman"
    green "  [OK] podman-launcher installed"
}

# ─── Image pull ──────────────────────────────────────────────────────

pull_images() {
    if ! command -v podman &>/dev/null; then
        yellow "  [!] podman not available, skipping image pull"
        return
    fi
    # On macOS, ensure machine is running before pulling
    if $IS_MACOS; then
        if ! podman machine list 2>/dev/null | grep -qi "currently running"; then
            yellow "  [!] podman machine not running, skipping image pull (will pull on first use)"
            return
        fi
    fi
    yellow "-> Pulling container images..."
    podman pull "$PG_IMAGE" 2>/dev/null && green "  [OK] PostgreSQL image pulled" || yellow "  [!] Failed to pull PostgreSQL image (will pull on first use)"
    podman pull "$BACKUP_IMAGE" 2>/dev/null && green "  [OK] Backup image pulled" || yellow "  [!] Failed to pull backup image (will pull on first use)"
}

check_path() {
    # Only need PATH setup when installed to a user-local directory
    if [ "$INSTALL_DIR" = "/usr/local/bin" ] || [ "$INSTALL_DIR" = "/usr/bin" ]; then
        return
    fi

    # Already in PATH — nothing to do
    if command -v "$BINARY" &>/dev/null; then
        return
    fi

    local rc_file=""
    local path_line="export PATH=\"$INSTALL_DIR:\$PATH\""

    # Detect shell and pick the right rc file
    if $IS_MACOS; then
        # macOS default shell is zsh
        rc_file="$HOME/.zshrc"
    else
        case "${SHELL:-}" in
            */zsh) rc_file="$HOME/.zshrc" ;;
            */fish) rc_file="$HOME/.config/fish/config.fish" ;;
            */bash|*/sh)
                if [ -f "$HOME/.bashrc" ]; then
                    rc_file="$HOME/.bashrc"
                elif [ -f "$HOME/.profile" ]; then
                    rc_file="$HOME/.profile"
                fi
                ;;
            *) rc_file="$HOME/.bashrc" ;;
        esac
    fi

    # Check if already present
    if [ -n "$rc_file" ] && [ -f "$rc_file" ] && grep -qF "$INSTALL_DIR" "$rc_file" 2>/dev/null; then
        yellow ""
        yellow "  NOTE: $INSTALL_DIR already in $rc_file, but not in current PATH."
        yellow "  Run: source $rc_file"
        yellow ""
        return
    fi

    if [ -n "$rc_file" ] && [ "$rc_file" != "$HOME/.config/fish/config.fish" ]; then
        printf '\n# Added by pgcli installer\n%s\n' "$path_line" >> "$rc_file"
        green "  [OK] Added $INSTALL_DIR to PATH in $rc_file"
        yellow "  Run: source $rc_file"
    else
        yellow ""
        yellow "  NOTE: Add $INSTALL_DIR to your PATH:"
        yellow "    $path_line"
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

    if $IS_LINUX; then
        install_podman_launcher
        setup_linux_deps
    elif $IS_MACOS; then
        setup_macos_deps
    fi

    pull_images
    check_path

    echo ""
    green "Installation complete!"
    echo ""
    echo "  Quick start:"
    echo "    pg config init --add default --base-dir /data/pg"
    echo "    pg start"
    echo "    pg status"
    echo ""

    if $IS_MACOS; then
        dim "  macOS notes:"
        dim "    - Podman runs inside a VM (podman machine). Use 'podman machine' to manage it."
        dim "    - For files outside your home directory, add volume mounts via podman machine."
        echo ""
    fi
}

main "$@"
