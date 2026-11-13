#!/usr/bin/env bash
# Export container images as docker-archive tar files in one flat directory.
# The image list comes from docs/images.md or, with --ansible, from the
# pgcli_images variable in an Ansible group_vars file. Archive names follow
# the offline manifest convention, so the directory can be served as-is by
# `pg http` or any static HTTP server.
#
# Images are pulled with --platform before saving: podman save cannot write a
# multi-arch manifest list to docker-archive. HTTP_PROXY / HTTPS_PROXY from the
# environment are inherited by podman.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

MANIFEST="$REPO_ROOT/docs/images.md"
ANSIBLE_FILE=""
MANIFEST_SET=false
ANSIBLE_SET=false
OUT="$REPO_ROOT/images"
PLATFORM="linux/amd64"
FORCE=false
DRY_RUN=false

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
warn() { printf 'WARN: %s\n' "$*" >&2; }

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Export container images as docker-archive tar files in a flat directory.

Image source (one of):
      --manifest FILE   Markdown table list (default: docs/images.md)
      --ansible FILE    pgcli_images list from an Ansible group_vars file;
                        archive names are taken from each entry

Options:
  -o, --output DIR      output directory (default: $OUT)
      --platform ARCH   platform to pull (default: $PLATFORM)
  -f, --force           re-export archives that already exist
  -n, --dry-run         print the planned exports without pulling or saving
  -h, --help            show this help

Archive names match the offline manifest convention:
  <repository-basename>_<tag>.tar    e.g. pgcli-pg_18-2.58.0.tar

After exporting, serve the directory with:
  pg http start --listen 0.0.0.0 --port 18080 --dir DIR
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -o|--output)
            [[ $# -ge 2 ]] || die "missing value for $1"
            OUT=$2; shift 2 ;;
        --output=*)   OUT=${1#*=}; shift ;;
        --platform)
            [[ $# -ge 2 ]] || die "missing value for $1"
            PLATFORM=$2; shift 2 ;;
        --platform=*) PLATFORM=${1#*=}; shift ;;
        -f|--force)   FORCE=true; shift ;;
        -n|--dry-run) DRY_RUN=true; shift ;;
        --manifest)
            [[ $# -ge 2 ]] || die "missing value for $1"
            MANIFEST=$2; MANIFEST_SET=true; shift 2 ;;
        --manifest=*) MANIFEST=${1#*=}; MANIFEST_SET=true; shift ;;
        --ansible)
            [[ $# -ge 2 ]] || die "missing value for $1"
            ANSIBLE_FILE=$2; ANSIBLE_SET=true; shift 2 ;;
        --ansible=*)  ANSIBLE_FILE=${1#*=}; ANSIBLE_SET=true; shift ;;
        -h|--help)    usage; exit 0 ;;
        *)            die "unknown option: $1 (try --help)" ;;
    esac
done

[[ $MANIFEST_SET == true && $ANSIBLE_SET == true ]] \
    && die "--manifest and --ansible are mutually exclusive"
[[ -n $OUT ]] || die "output directory must not be empty"
[[ -n $PLATFORM ]] || die "platform must not be empty"
if [[ $ANSIBLE_SET == true ]]; then
    [[ -r $ANSIBLE_FILE ]] || die "cannot read Ansible file: $ANSIBLE_FILE"
else
    [[ -r $MANIFEST ]] || die "cannot read image manifest: $MANIFEST"
fi

images=()
archives=()
declare -A seen=()
declare -A archive_owner=()

derive_archive() {
    local name=${1##*/}
    printf '%s.tar\n' "${name//:/_}"
}

add_entry() {
    local image=$1 archive=$2
    [[ -n $image ]] || return 0
    [[ -n ${seen[$image]:-} ]] && return 0
    [[ $archive =~ ^[[:alnum:]._+-]+\.tar$ ]] \
        || die "invalid archive name for $image: '$archive'"
    if [[ -n ${archive_owner[$archive]:-} ]]; then
        die "archive name collision: '$archive' is used by both ${archive_owner[$archive]} and $image"
    fi
    seen[$image]=1
    archive_owner[$archive]=$image
    images+=("$image")
    archives+=("$archive")
}

if [[ $ANSIBLE_SET == true ]]; then
    # Extract image/archive pairs from the pgcli_images block. Entries are
    # "- image: ..." lines; archive is an optional following key.
    while IFS=$'\t' read -r image archive; do
        [[ -n $archive ]] || archive=$(derive_archive "$image")
        add_entry "$image" "$archive"
    done < <(
        awk -F: '
            function trim(v) {
                gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
                gsub(/^["\047]|["\047]$/, "", v)
                return v
            }
            function flush() { if (have) print img "\t" arch; have = 0 }
            /^pgcli_images:[[:space:]]*$/ { in_block = 1; next }
            in_block {
                if ($0 ~ /^[^[:space:]#]/) { flush(); in_block = 0; next }
                if ($1 ~ /^[[:space:]]*- image$/) {
                    flush()
                    img = trim(substr($0, index($0, ":") + 1))
                    arch = ""; have = 1
                } else if ($1 ~ /^[[:space:]]*image$/) {
                    img = trim(substr($0, index($0, ":") + 1))
                } else if ($1 ~ /^[[:space:]]*archive$/) {
                    arch = trim(substr($0, index($0, ":") + 1))
                }
            }
            END { if (in_block) flush() }
        ' "$ANSIBLE_FILE"
    )
else
    # Table rows carry the repository and tag in the first two columns:
    #   | `ghcr.io/mars-base/pgcli/pgcli-pg` | `18-2.58.0` | ...
    while IFS= read -r image; do
        add_entry "$image" "$(derive_archive "$image")"
    done < <(
        awk -F'|' '
            /^\|[[:space:]]*`/ {
                img = $2; tag = $3
                gsub(/`|[[:space:]]/, "", img)
                gsub(/`|[[:space:]]/, "", tag)
                if (img != "" && tag != "") print img ":" tag
            }
        ' "$MANIFEST"
    )
fi

(( ${#images[@]} > 0 )) || die "no images found in ${ANSIBLE_FILE:-$MANIFEST}"

if [[ $DRY_RUN == true ]]; then
    for i in "${!images[@]}"; do
        printf 'WOULD  %s -> %s/%s\n' "${images[$i]}" "$OUT" "${archives[$i]}"
    done
    printf '\nDry run: %d image(s); nothing pulled or saved.\n' "${#images[@]}"
    exit 0
fi

command -v podman >/dev/null 2>&1 || die "podman not found in PATH"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

exported=0
skipped=0
failed=0
failures=()

for i in "${!images[@]}"; do
    image=${images[$i]}
    archive="$OUT/${archives[$i]}"

    if [[ -f $archive && $FORCE != true ]]; then
        printf 'SKIP    %s (exists)\n' "$archive"
        skipped=$((skipped + 1))
        continue
    fi

    printf 'PULL    %s (%s)\n' "$image" "$PLATFORM"
    if ! podman pull --platform "$PLATFORM" "$image" >/dev/null 2>&1; then
        if podman image exists "$image" 2>/dev/null; then
            warn "$image: pull failed, using the local image"
        else
            printf 'FAIL    %s: pull failed and no local image\n' "$image" >&2
            failures+=("$image")
            failed=$((failed + 1))
            continue
        fi
    fi

    tmp="$archive.tmp"
    rm -f "$tmp"
    if podman save --format docker-archive -o "$tmp" "$image" >/dev/null 2>&1; then
        mv -f "$tmp" "$archive"
        printf 'OK      %s\n' "$archive"
        exported=$((exported + 1))
    else
        rm -f "$tmp"
        printf 'FAIL    %s: podman save failed\n' "$image" >&2
        failures+=("$image")
        failed=$((failed + 1))
    fi
done

printf '\nExported: %d  Skipped: %d  Failed: %d\n' "$exported" "$skipped" "$failed"
printf 'Output:   %s\n' "$OUT"

if (( failed > 0 )); then
    printf 'Failed images:\n' >&2
    printf '  %s\n' "${failures[@]}" >&2
    exit 1
fi

cat <<EOF

Serve this directory as an offline image source:
  pg http start --listen 0.0.0.0 --port 18080 --dir "$OUT"

Then set pgcli_images_http_url in group_vars/all/main.yml to:
  http://<server>:18080
EOF
