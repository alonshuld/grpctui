.DEFAULT_GOAL := help

GO      ?= go
BINARY  := grpctui
PKG     := ./cmd/grpctui

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary
	$(GO) build -o $(BINARY) $(PKG)

.PHONY: run
run: ## Run against a target: make run TARGET=localhost:50051
	$(GO) run $(PKG) $(TARGET)

.PHONY: test
test: ## Run the test suite
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Write and open an HTML coverage report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

.PHONY: golden
golden: ## Regenerate teatest golden files (review the diff!)
	$(GO) test ./internal/ui -update

.PHONY: fmt
fmt: ## Format the tree
	gofmt -w .

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: check
check: ## Everything CI enforces, locally
	@test -z "$$(gofmt -l . | grep -v '^agent/')" || { gofmt -l . | grep -v '^agent/'; exit 1; }
	$(GO) vet ./...
	golangci-lint run
	$(GO) build ./...
	$(GO) test -race ./...

.PHONY: hooks
hooks: ## Install the pre-push hook
	@mkdir -p .git/hooks
	@cp scripts/hooks/pre-push .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "installed .git/hooks/pre-push"

.PHONY: snapshot
snapshot: ## Dry-run the release build
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BINARY) dist coverage.out
