BINARY = pg
MODULE = github.com/mars-base/pgcli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X $(MODULE)/internal/cli.version=$(VERSION) \
	-X $(MODULE)/internal/cli.commit=$(COMMIT) \
	-X $(MODULE)/internal/cli.buildDate=$(DATE)

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build clean test lint install build-all container-build container-push

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

install:
	go install -ldflags "$(LDFLAGS)" .

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
	podman build -t ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0 -f embed/Containerfile embed/

container-build-backup:
	podman build -t ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0 -f embed/backup.Containerfile embed/

container-push:
	podman push ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0
	podman push ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0
