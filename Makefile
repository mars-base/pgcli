BINARY = pg
MODULE = github.com/mars-base/pgcli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(DATE)

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build clean test lint install build-all container-build container-push container-build-minio container-push-minio

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
