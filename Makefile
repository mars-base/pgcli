BINARY = pg
MODULE = github.com/mars-base/pgcli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(DATE)

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build clean test lint install build-all container-build container-push container-build-minio container-push-minio container-build-mc container-push-mc

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

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

# Single-node MinIO image (public, referenced by config.DefaultMinioImageTag).
# Built from the upstream .deb's static binary on Alpine — the official MinIO
# image dropped the web console. amd64 only, no manifest. The deb is not in the
# repo; place it at MINIO_DEB (default below) or override:
#   make container-build-minio MINIO_DEB=/path/to/minio_<version>_amd64.deb
# `embed/minio` is gitignored scratch: extracted before the build, deleted after.
# The apk step needs outbound network: prefix with HTTP_PROXY/HTTPS_PROXY if the
# build host routes through a proxy.
MINIO_DEB   ?= /tmp/minio_20250422221226.0.0_amd64.deb
MINIO_IMAGE  = ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226

container-build-minio:
	dpkg-deb --fsys-tarfile $(MINIO_DEB) | tar -xO ./usr/local/bin/minio > embed/minio
	chmod 0755 embed/minio
	podman build --platform linux/amd64 -t $(MINIO_IMAGE) -f embed/minio.Containerfile embed/
	rm -f embed/minio

container-push-minio:
	podman push $(MINIO_IMAGE)

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
