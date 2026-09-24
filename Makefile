BINARY = pg
MODULE = github.com/mars-base/pgcli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(DATE)

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build clean test lint install build-all gencert container-build container-push container-build-minio container-push-minio container-build-mc container-push-mc container-build-patroni container-push-patroni container-build-rustfs container-push-rustfs

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

gencert:
	go build -o bin/gencert ./tools/gencert

install:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH)/bin/$(BINARY) .

build-all:
	@for target in $(PLATFORMS); do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		echo "Building $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-$$os-$$arch . ; \
	done

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ dist/

container-build:
	podman build --platform linux/amd64 -t ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-amd64 -f embed/Containerfile embed/
	podman build --platform linux/arm64 -t ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-arm64 -f embed/Containerfile embed/

container-build-backup:
	podman build --platform linux/amd64 -t ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0-amd64 -f embed/backup.Containerfile embed/
	podman build --platform linux/arm64 -t ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0-arm64 -f embed/backup.Containerfile embed/

container-build-patroni:
	podman build --http-proxy=false -t ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5 -f embed/patroni.Containerfile embed/

container-manifest:
	podman manifest rm ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 2>/dev/null || true
	podman manifest create ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 \
		ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-amd64 \
		ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-arm64
	podman manifest rm ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 2>/dev/null || true
	podman manifest create ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 \
		ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0-amd64 \
		ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0-arm64

container-push:
	podman manifest push ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 --all
	podman manifest push ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 --all
	podman push ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5 ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5

# Single-node MinIO image (public, referenced by config.DefaultMinioImageTag).
# Built from the upstream static binaries (minio/minio GitHub releases) on
# Alpine — the official MinIO image dropped the web console. Dual-arch
# (amd64+arm64) with a manifest list, so a pull-only install works on arm64
# hosts too (e.g. Apple Silicon podman VMs). The tag mirrors the release date;
# binaries are downloaded and sha256-verified by the target, never committed.
# The apk step and the alpine arm64 base pull need outbound network: prefix
# with HTTP_PROXY/HTTPS_PROXY if the build host routes through a proxy.
MINIO_VERSION ?= RELEASE.2025-04-22T22-12-26Z
# Compact timestamp tag (20250422221226): strip letters/dots/colons/dashes.
MINIO_TS    := $(shell echo $(MINIO_VERSION) | tr -d 'A-Z.:-')
MINIO_IMAGE  = ghcr.io/mars-base/pgcli/pgcli-minio:$(MINIO_TS)
MINIO_BASE   = https://github.com/minio/minio/releases/download/$(MINIO_VERSION)

container-build-minio:
	@for a in amd64 arm64; do \
	  curl -sfL --retry 3 -o embed/minio.linux-$$a $(MINIO_BASE)/minio.linux-$$a.$(MINIO_VERSION) || exit 1; \
	  curl -sfL --retry 3 -o /tmp/minio.linux-$$a.sha256sum $(MINIO_BASE)/minio.linux-$$a.$(MINIO_VERSION).sha256sum || exit 1; \
	  ( cd embed && echo "$$(awk '{print $$1}' /tmp/minio.linux-$$a.sha256sum)  minio.linux-$$a" | sha256sum -c ) || exit 1; \
	  chmod 0755 embed/minio.linux-$$a; \
	  mv embed/minio.linux-$$a embed/minio; \
	  podman build --platform linux/$$a --manifest $(MINIO_IMAGE) -t $(MINIO_IMAGE)-$$a -f embed/minio.Containerfile embed/ || exit 1; \
	  rm -f embed/minio; \
	done

container-push-minio:
	podman manifest push $(MINIO_IMAGE) $(MINIO_IMAGE) --all

# MinIO client image — a single static binary on scratch, ~30 MB per arch. The
# upstream binaries (minio/mc GitHub releases) are statically linked and
# stripped, so nothing to layer; only a TLS CA bundle is copied from the build
# host. Dual-arch (amd64+arm64) with a manifest tag — arm64 gives Apple Silicon
# hosts (podman machine VMs) native execution. Binaries are downloaded and
# sha256-verified by the target, never committed.
MC_VERSION ?= RELEASE.2025-08-13T08-35-41Z
# Compact timestamp tag, same style as the minio image (20250422221226):
# strip letters/dots/colons/dashes from RELEASE.2025-08-13T08-35-41Z.
MC_TS   := $(shell echo $(MC_VERSION) | tr -d 'A-Z.:-')
MC_IMAGE = ghcr.io/mars-base/pgcli/pgcli-mc:$(MC_TS)
MC_BASE  = https://github.com/minio/mc/releases/download/$(MC_VERSION)

container-build-mc:
	cp /etc/ssl/certs/ca-certificates.crt embed/ca-certificates.crt
	@for a in amd64 arm64; do \
	  curl -sfL --retry 3 -o embed/mc.linux-$$a $(MC_BASE)/mc.linux-$$a.$(MC_VERSION) || exit 1; \
	  curl -sfL --retry 3 -o /tmp/mc.linux-$$a.sha256sum $(MC_BASE)/mc.linux-$$a.$(MC_VERSION).sha256sum || exit 1; \
	  ( cd embed && echo "$$(awk '{print $$1}' /tmp/mc.linux-$$a.sha256sum)  mc.linux-$$a" | sha256sum -c ) || exit 1; \
	  chmod 0755 embed/mc.linux-$$a; \
	  mv embed/mc.linux-$$a embed/mc; \
	  podman build --platform linux/$$a --manifest $(MC_IMAGE) -t $(MC_IMAGE)-$$a -f embed/mc.Containerfile embed/ || exit 1; \
	  rm -f embed/mc; \
	done
	rm -f embed/ca-certificates.crt

container-push-mc:
	podman manifest push $(MC_IMAGE) $(MC_IMAGE) --all

# rustfs wrapper image — the ONLY way pgcli runs rustfs (config.DefaultRustfsImageTag
# points here). Upstream rustfs bakes a fixed non-root user (uid/gid 10001), which
# under rootless podman forces a host-side ownership dance to pre-own bind mounts.
# This image removes that: its entrypoint starts as container-root, chowns the
# bind-mounted data/cert dirs to 10001, then `su`-drops to rustfs and execs the
# UNMODIFIED upstream /entrypoint.sh (see embed/rustfs-entrypoint.sh). pgcli then
# runs it with no --user flag and no host ownership code.
#
# The rustfs base is multi-arch, so — unlike the minio/mc targets that stage
# per-arch static binaries — there is nothing to download: just build the same
# thin wrapper once per arch (each build resolves the base for its own platform)
# and merge into a manifest. The tag mirrors the pinned upstream rustfs version.
RUSTFS_VERSION ?= 1.0.0
RUSTFS_BASE    = docker.io/rustfs/rustfs:$(RUSTFS_VERSION)
RUSTFS_IMAGE   = ghcr.io/mars-base/pgcli/pgcli-rustfs:$(RUSTFS_VERSION)

# `--manifest` APPENDS a per-arch entry to an existing manifest list (it does not
# replace), so without clearing first, re-running this target on the same host
# accumulates stale entries — a later `podman run` picks whichever entry the
# resolver visits first, potentially an older build. Drop the list (and the two
# per-arch tags) before rebuilding.
container-build-rustfs:
	podman manifest rm $(RUSTFS_IMAGE) 2>/dev/null || true
	podman rmi -f $(RUSTFS_IMAGE)-amd64 $(RUSTFS_IMAGE)-arm64 2>/dev/null || true
	@for a in amd64 arm64; do \
	  podman build --platform linux/$$a --manifest $(RUSTFS_IMAGE) -t $(RUSTFS_IMAGE)-$$a \
	    --build-arg RUSTFS_BASE=$(RUSTFS_BASE) -f embed/rustfs.Containerfile embed/ || exit 1; \
	done

container-push-rustfs:
	podman manifest push $(RUSTFS_IMAGE) $(RUSTFS_IMAGE) --all
