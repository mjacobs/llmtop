# llmtop — developer task runner.
#
# Build and test need cgo for the NVML bindings (see README "Install / build"),
# so CGO_ENABLED is forced on here and overridable from the environment.
# Run `make` or `make help` for the target list.

BINARY       := llmtop
PKG          := ./cmd/llmtop
COVERPROFILE := coverage.out
XDG_BIN_HOME ?= $(HOME)/.local/bin

# cgo is required to link libnvidia-ml; keep it on unless the caller overrides.
CGO_ENABLED ?= 1
export CGO_ENABLED

# Version stamped into the binary (`llmtop version`, the TUI header, and the
# agent's advertised agent_version). Defaults to the nearest tag, falling back
# to the short commit hash; packagers can override with `make VERSION=1.2.3`.
# Binaries built with plain `go build`/`go install` fall back to the module
# version Go records in the build info instead.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -X main.version=$(VERSION)

# Passthrough knobs: `make run ARGS=--demo`, `make test GOFLAGS=-count=1`.
GOFLAGS ?=
ARGS    ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the llmtop binary (cgo/NVML)
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

.PHONY: install
install: build ## Build and install llmtop into XDG_BIN_HOME
	install -Dm755 $(BINARY) "$(XDG_BIN_HOME)/$(BINARY)"

.PHONY: uninstall
uninstall: ## Remove the installed binary from XDG_BIN_HOME
	rm -f "$(XDG_BIN_HOME)/$(BINARY)"

.PHONY: run
run: build ## Build then run (args via ARGS=..., e.g. make run ARGS=--demo)
	./$(BINARY) $(ARGS)

.PHONY: test
test: ## Run the unit tests
	go test $(GOFLAGS) ./...

.PHONY: test-nvml
test-nvml: ## Run the env-gated NVML hardware smoke test (needs a real GPU)
	LLMTOP_NVML_TEST=1 go test $(GOFLAGS) ./internal/gpu/ -run NVML -v

.PHONY: cover
cover: ## Run tests with a coverage profile and print the total
	go test $(GOFLAGS) -coverprofile=$(COVERPROFILE) ./...
	go tool cover -func=$(COVERPROFILE) | tail -1

.PHONY: lint
lint: ## Run golangci-lint (v2; CI pins v2.12 — see .github/workflows/ci.yml)
	golangci-lint run

.PHONY: fmt
fmt: ## Format all Go sources in place
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-clean
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet $(GOFLAGS) ./...

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

.PHONY: check
check: fmt-check vet lint test ## Run fmt-check, vet, lint, and tests (CI gate)

.PHONY: screenshot
screenshot: build ## Regenerate docs/demo.png from --demo (needs tmux, freeze, magick, optipng)
	scripts/demo-screenshot.sh

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -f $(BINARY) $(COVERPROFILE)
	go clean
