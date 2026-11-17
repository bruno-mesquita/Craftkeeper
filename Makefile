# Pinned toolchain. Go is NOT installed on the host: everything Go runs in a throwaway container.
GO_IMAGE         := golang:1.27.4-trixie
TAILWIND_VERSION := v4.3.5
TAILWIND_SHA256  := e47695cee3f0d90623a91f3939b08bdee610eb5ad99340adff9d4145b284d6cc
TAILWIND_BIN     := tools/tailwindcss-$(TAILWIND_VERSION)

# The host user has no access to docker.sock. Run `sudo make <target>` from a real terminal
# (sudo cannot prompt inside Claude Code). Under sudo, SUDO_UID/SUDO_GID name the real user,
# so generated files are not root-owned.
DOCKER   ?= docker
HOST_UID := $(or $(SUDO_UID),$(shell id -u))
HOST_GID := $(or $(SUDO_GID),$(shell id -g))

# Output of Go targets is also written here so Claude can read it without copy/paste.
LOG    := .cache/last-run.log
SHELL  := /bin/bash
.SHELLFLAGS := -o pipefail -c

# Runs as the invoking user so generated files (bin/, go.sum) are not root-owned.
# Module and build caches live in .cache/ (gitignored) for the same reason.
GO_RUN = $(DOCKER) run --rm \
	-u $(HOST_UID):$(HOST_GID) \
	-v $(CURDIR):/src -w /src \
	-e HOME=/tmp \
	-e GOMODCACHE=/src/.cache/mod \
	-e GOCACHE=/src/.cache/build \
	-e GOFLAGS=-mod=mod \
	-e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
	$(GO_IMAGE)

.PHONY: check build test vet fmt tidy css run clean

# Only our sources: never scan the module cache in .cache/.
GO_DIRS := cmd internal migrations web

# Everything CI would run: formatting, vet, tests, build. Fails if gofmt reports files.
check: | .cache
	$(GO_RUN) sh -c 'set -e; f=$$(gofmt -l $(GO_DIRS)); if [ -n "$$f" ]; then echo "gofmt needed:"; echo "$$f"; exit 1; fi; \
		go vet ./... && go test ./... && go build -trimpath -ldflags="-s -w" -o bin/mesquita-panel ./cmd/panel' 2>&1 | tee $(LOG)

build: | .cache
	$(GO_RUN) go build -trimpath -ldflags="-s -w" -o bin/mesquita-panel ./cmd/panel 2>&1 | tee $(LOG)

test: | .cache
	$(GO_RUN) go test ./... 2>&1 | tee $(LOG)

vet: | .cache
	$(GO_RUN) go vet ./... 2>&1 | tee $(LOG)

.cache:
	mkdir -p .cache

fmt:
	$(GO_RUN) gofmt -l -w $(GO_DIRS)

tidy:
	$(GO_RUN) go mod tidy

# Tailwind standalone CLI (no Node). Output is committed and embedded; production never builds CSS.
css: $(TAILWIND_BIN)
	$(TAILWIND_BIN) -i web/styles/app.css -o web/assets/css/app.css --minify

$(TAILWIND_BIN):
	mkdir -p tools
	curl -fsSL -o $@.tmp https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-linux-x64
	echo "$(TAILWIND_SHA256)  $@.tmp" | sha256sum -c -
	chmod +x $@.tmp && mv $@.tmp $@

# Runs the built binary on the host (no Go needed) against a throwaway data dir.
# Not dependent on build: building needs sudo, running must not.
run:
	MESQUITA_DATA_DIR=$(CURDIR)/.data MESQUITA_LISTEN=127.0.0.1:8090 MESQUITA_LOG_LEVEL=debug ./bin/mesquita-panel

clean:
	rm -rf bin .data
