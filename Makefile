BINARY = pg
MODULE = github.com/mars-base/pgcli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(DATE)

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build clean test lint install build-all gencert container-build container-push container-build-minio container-push-minio container-build-mc container-push-mc container-build-patroni container-push-patroni container-build-rustfs container-push-rustfs container-build-predixy container-push-predixy container-build-influxdb container-push-influxdb container-build-influx2tsdb container-push-influx2tsdb container-images-export

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

# Predixy proxy image — built from the upstream FREE-EDITION binary on Alpine.
# Upstream ships ONLY an amd64 glibc binary, so (unlike the dual-arch
# minio/mc/rustfs targets) this is a single-arch image with no manifest list;
# the tag carries a -alpine suffix to say so. The tarball's own license.conf is
# already expired (the free binary refuses to start with it), so we always
# overwrite it with the current license2026.conf from the release. The binary
# and extracted conf are downloaded and sha256-verified by the target, staged
# into embed/, and deleted after — never committed. The apk step and the alpine
# base pull need outbound network: prefix HTTP_PROXY/HTTPS_PROXY, or add
# `--network host` to the build line on a host whose default container network
# can't reach the alpine CDN (same caveat as the minio/mc targets above).
PREDIXY_VERSION ?= 7.0.1
PREDIXY_IMAGE   = ghcr.io/mars-base/pgcli/predixy:$(PREDIXY_VERSION)-alpine
PREDIXY_BASE    = https://github.com/joyieldInc/predixy/releases/download/$(PREDIXY_VERSION)
PREDIXY_LICENSE_SHA256 ?= 04390ed707c305a64827b61e5ce71f1d2c53e850ccb30c769e6c74957c2be400

container-build-predixy:
	@curl -sfL --retry 3 -o /tmp/predixy.tar.gz $(PREDIXY_BASE)/predixyFreeEdition-$(PREDIXY_VERSION)-amd64-linux.tar.gz || exit 1
	@rm -rf /tmp/predixy-ex && mkdir -p /tmp/predixy-ex && tar xzf /tmp/predixy.tar.gz -C /tmp/predixy-ex
	@cp /tmp/predixy-ex/predixyFreeEdition-$(PREDIXY_VERSION)/bin/predixy embed/predixy
	@rm -rf embed/predixy-conf && cp -r /tmp/predixy-ex/predixyFreeEdition-$(PREDIXY_VERSION)/conf embed/predixy-conf
	@curl -sfL --retry 3 -o embed/predixy-conf/license.conf $(PREDIXY_BASE)/license2026.conf || exit 1
	@echo "$(PREDIXY_LICENSE_SHA256)  embed/predixy-conf/license.conf" | sha256sum -c - || exit 1
	@podman build --platform linux/amd64 -t $(PREDIXY_IMAGE) -f embed/predixy.Containerfile embed/
	@rm -f embed/predixy
	@rm -rf embed/predixy-conf
	@rm -rf /tmp/predixy.tar.gz /tmp/predixy-ex

container-push-predixy:
	podman push $(PREDIXY_IMAGE) $(PREDIXY_IMAGE)

# InfluxDB 1.13.1 image — single-node OSS on Alpine, packaging the official
# linux/amd64 release tarball (influxd + influx + influx_inspect). Single-arch:
# the addon targets Linux x86 only. INFLUXDB_PACKAGE may point at a local copy
# of the tarball for offline builds; the sha256 is verified either way.
INFLUXDB_VERSION ?= 1.13.1
INFLUXDB_IMAGE    = ghcr.io/mars-base/pgcli/pgcli-influxdb:$(INFLUXDB_VERSION)
INFLUXDB_URL      = https://dl.influxdata.com/influxdb/releases/v$(INFLUXDB_VERSION)/influxdb-$(INFLUXDB_VERSION)_linux_amd64.tar.gz
INFLUXDB_SHA256  ?= a8a6e648f5271e3286bc965a2d321c86c6404afaa12ca4bf37c44303e0bf2f52
INFLUXDB_PACKAGE ?=

container-build-influxdb:
	@src="$(INFLUXDB_PACKAGE)"; \
	if [ -z "$$src" ]; then \
	  src=/tmp/influxdb-$(INFLUXDB_VERSION)_linux_amd64.tar.gz; \
	  curl -sfL --retry 3 -o "$$src" $(INFLUXDB_URL) || exit 1; \
	fi; \
	echo "$(INFLUXDB_SHA256)  $$src" | sha256sum -c - || exit 1; \
	rm -rf /tmp/influxdb-ex && mkdir -p /tmp/influxdb-ex; \
	tar xzf "$$src" -C /tmp/influxdb-ex || exit 1; \
	d=$$(find /tmp/influxdb-ex -mindepth 1 -maxdepth 1 -type d -name 'influxdb-*' | head -n1); \
	[ -n "$$d" ] || { echo "influxdb package layout not recognised" >&2; exit 1; }; \
	cp "$$d/influxd" "$$d/influx" "$$d/influx_inspect" "$$d/influxdb.conf" embed/ || exit 1; \
	chmod 0755 embed/influxd embed/influx embed/influx_inspect; \
	podman build --platform linux/amd64 -t $(INFLUXDB_IMAGE) -f embed/influxdb.Containerfile embed/; \
	rc=$$?; rm -f embed/influxd embed/influx embed/influx_inspect embed/influxdb.conf; \
	rm -rf /tmp/influxdb-ex; exit $$rc

container-push-influxdb:
	podman push $(INFLUXDB_IMAGE) $(INFLUXDB_IMAGE)

# influx2tsdb-proxy image — a single static Go binary from the upstream
# linux/amd64 release asset. Single-arch: the addon targets Linux x86 only.
# INFLUX2TSDB_BINARY may point at a local copy of the release binary for
# offline builds; the sha256 is verified either way.
INFLUX2TSDB_VERSION ?= 1.3.2
INFLUX2TSDB_IMAGE    = ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:$(INFLUX2TSDB_VERSION)
INFLUX2TSDB_URL      = https://github.com/mars-base/influx2tsdb-proxy/releases/download/v$(INFLUX2TSDB_VERSION)/influx2tsdb-proxy-linux-amd64
INFLUX2TSDB_SHA256  ?= 83d9e79c716e20a67b56265781cc2222ea37077c7793736ebb2bfb996f0e7481
INFLUX2TSDB_BINARY  ?=

container-build-influx2tsdb:
	@src="$(INFLUX2TSDB_BINARY)"; \
	if [ -z "$$src" ]; then \
	  src=/tmp/influx2tsdb-proxy-linux-amd64; \
	  curl -sfL --retry 3 -o "$$src" $(INFLUX2TSDB_URL) || exit 1; \
	fi; \
	echo "$(INFLUX2TSDB_SHA256)  $$src" | sha256sum -c - || exit 1; \
	cp "$$src" embed/ && chmod 0755 embed/influx2tsdb-proxy-linux-amd64 || exit 1; \
	podman build --platform linux/amd64 -t $(INFLUX2TSDB_IMAGE) -f embed/influx2tsdb.Containerfile embed/; \
	rc=$$?; rm -f embed/influx2tsdb-proxy-linux-amd64; exit $$rc

container-push-influx2tsdb:
	podman push $(INFLUX2TSDB_IMAGE) $(INFLUX2TSDB_IMAGE)

# Offline image export for the docs/images.md catalog (see that doc's
# "镜像导出" section for the manual equivalents and the rustfs/redis
# manifest-list caveat). Always pulls --platform linux/amd64 FIRST so a
# multi-arch manifest-list tag in the local store (e.g. pgcli-rustfs) is
# normalized to a single-arch image before `podman save` — docker-archive
# format refuses manifest lists. Tars land in ./images/ (gitignored) named
# <repo>_<tag>.tar with canonical RepoTags, so `podman load` on the target
# yields the exact reference pgcli expects — no retagging there. The ghcr.io
# mars-base images are private: `podman login ghcr.io` must have happened
# first (or pull is skipped when the image is already local).
# Keep this list in sync with docs/images.md.
IMAGES := \
	ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 \
	ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5 \
	ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 \
	ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226 \
	ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541 \
	ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0 \
	ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1 \
	ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:1.3.2 \
	ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine \
	docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z \
	docker.io/postgrest/postgrest:v16.3 \
	docker.io/edoburu/pgbouncer:v1.25.2-p0 \
	docker.io/library/haproxy:3.2.23-alpine \
	quay.io/coreos/etcd:v3.5.30 \
	ghcr.io/pgdogdev/pgdog:v0.1.57 \
	docker.io/library/redis:7.4.11 \
	docker.io/library/redis:8.10.2 \
	docker.io/dpage/pgadmin4:9.18 \
	docker.io/library/nginx:1.27-alpine \
	docker.io/dxflrs/garage:v2.4.1 \
	docker.io/chrislusf/seaweedfs:4.48_large_disk \
	docker.io/grafana/grafana:13.2.4 \
	docker.io/prom/prometheus:v3.15.0 \
	docker.io/prom/alertmanager:v0.34.1 \
	docker.io/gitea/gitea:28.1.0 \
	ghcr.io/thinkparq/beegfs-mgmtd:8.4.0 \
	ghcr.io/thinkparq/beegfs-meta:8.4.0 \
	ghcr.io/thinkparq/beegfs-storage:8.4.0 \
	docker.io/victoriametrics/victoria-metrics:v1.154.1 \
	docker.io/victoriametrics/vminsert:v1.154.1-cluster \
	docker.io/victoriametrics/vmselect:v1.154.1-cluster \
	docker.io/victoriametrics/vmstorage:v1.154.1-cluster

container-images-export:
	@mkdir -p images
	@for img in $(IMAGES); do \
	  tar=images/$$(echo $${img##*/} | tr : _).tar; \
	  if [ -f $$tar ]; then echo "SKIP $$tar  (already exported)"; continue; fi; \
	  podman pull --platform linux/amd64 $$img >/dev/null 2>&1 \
	    || podman image exists $$img \
	    || { echo "FAIL: cannot resolve $$img (pull --platform amd64 failed and not in local store; login ghcr.io if private)" >&2; exit 1; }; \
	  podman save --format docker-archive -o $$tar $$img || exit 1; \
	  echo "OK   $$tar  <- $$img"; \
	done
