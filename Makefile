NAME  := bivouac
BIN   := out/$(NAME)
GOBIN ?= $(HOME)/go/bin

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS    := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

.PHONY: help all build install install-dev generate vet lint test clean
.DEFAULT_GOAL := help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

all: vet lint test build ## Run vet, lint, test, then build

build: ## Build the bivouac binary
	mkdir -p out
	nix develop --command go build -ldflags "$(LDFLAGS)" -o $(BIN) .

install: ## Install via goreleaser (matches release binary exactly)
	@if ! command -v goreleaser >/dev/null 2>&1; then \
		echo "error: goreleaser not installed; see https://goreleaser.com/install/"; \
		exit 1; \
	fi
	goreleaser build --single-target --clean --output $(BIN)
	install -d $(GOBIN)
	install -m 0755 $(BIN) $(GOBIN)/$(NAME)
	@echo "installed: $(GOBIN)/$(NAME)"

install-dev: ## Install quickly without goreleaser (dev iteration)
	mkdir -p out
	nix develop --command go build -ldflags "$(LDFLAGS)" -o $(BIN) .
	install -d $(GOBIN)
	install -m 0755 $(BIN) $(GOBIN)/$(NAME)
	@echo "installed (dev): $(GOBIN)/$(NAME) ($(VERSION))"

generate: ## Regenerate Pkl-derived config types
	nix develop --command go generate ./...

vet: ## Run go vet
	nix develop --command go vet ./...

lint: ## Run golangci-lint
	nix develop --command golangci-lint run ./...

test: ## Run go test
	nix develop --command go test ./...

clean: ## Remove the local binary
	rm -rf out
