NAME  := cloudlab
BIN   := out/$(NAME)
GOBIN ?= $(HOME)/go/bin

.PHONY: help all build install generate vet lint test clean
.DEFAULT_GOAL := help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

all: vet lint test build ## Run vet, lint, test, then build

build: ## Build the cloudlab binary
	mkdir -p out
	nix develop --command go build -o $(BIN) .

install: build ## Build and install to $GOBIN (default ~/go/bin)
	install -d $(GOBIN)
	install -m 0755 $(BIN) $(GOBIN)/$(NAME)

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
