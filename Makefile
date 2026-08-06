# llmtop — developer task runner.
#
# Build and test need cgo for the NVML bindings (see README "Install / build"),
# so CGO_ENABLED is forced on here and overridable from the environment.
# Run `make` or `make help` for the target list.

BINARY       := llmtop
PKG          := ./cmd/llmtop
COVERPROFILE := coverage.out

# cgo is required to link libnvidia-ml; keep it on unless the caller overrides.
CGO_ENABLED ?= 1
export CGO_ENABLED

# Passthrough knobs: `make run ARGS=--demo`, `make test GOFLAGS=-count=1`.
GOFLAGS ?=
ARGS    ?=

# Agent deployment target host (ssh destination). The binary is built here
# and copied over, so build-host glibc must be <= the agent host's (cgo).
DEPLOY_HOST ?= 192.0.2.5
AGENT_UNIT  := contrib/llmtop-agent.service

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the llmtop binary (cgo/NVML)
	go build $(GOFLAGS) -o $(BINARY) $(PKG)

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
lint: ## Run golangci-lint
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

# The pkill clears a hand-started agent so the service can bind the port, but
# only one owned by the ssh user — another user's agent survives (EPERM is
# silent) and the service will crash-loop on "address already in use" until
# that process is killed by its owner.
.PHONY: install-agent
install-agent: build ## One-time agent bootstrap on DEPLOY_HOST (binary + unit, enable --now)
	ssh $(DEPLOY_HOST) 'mkdir -p ~/.local/bin ~/.config/systemd/user'
	scp $(BINARY) $(DEPLOY_HOST):.local/bin/$(BINARY).new
	scp $(AGENT_UNIT) $(DEPLOY_HOST):.config/systemd/user/
	ssh $(DEPLOY_HOST) 'mv ~/.local/bin/$(BINARY).new ~/.local/bin/$(BINARY) \
		&& { pkill -f "[l]lmtop agent" 2>/dev/null || true; } \
		&& systemctl --user daemon-reload \
		&& systemctl --user enable --now llmtop-agent'

.PHONY: deploy-agent
deploy-agent: build ## Build and push the agent binary to DEPLOY_HOST, restart the service
	scp $(BINARY) $(DEPLOY_HOST):.local/bin/$(BINARY).new
	ssh $(DEPLOY_HOST) 'mv ~/.local/bin/$(BINARY).new ~/.local/bin/$(BINARY) \
		&& systemctl --user restart llmtop-agent'

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -f $(BINARY) $(COVERPROFILE)
	go clean
