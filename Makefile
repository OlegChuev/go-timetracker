# Telegram time tracker — development tasks.
#
# Run `make` on its own to list the available targets.

BINARY      := timetracker
BUILD_DIR   := bin
DB_PATH     ?= data/timetracker.db
GO          ?= go
LDFLAGS     := -s -w

.DEFAULT_GOAL := help
.PHONY: help build build-mac run dev test test-race cover bench lint fmt vet tidy deps clean reset-db db docker

help: ## Show this help
	@echo "Telegram time tracker"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Configuration lives in .env (copy .env.example to start)."

build: ## Compile the bot into bin/
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) .
	@echo "built $(BUILD_DIR)/$(BINARY)"

build-mac: ## Cross-compile for Apple Silicon (macOS arm64)
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath \
		-ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 .
	@echo "built $(BUILD_DIR)/$(BINARY)-darwin-arm64"

run: ## Run the bot (reads .env)
	$(GO) run .

dev: ## Run the bot with debug logging
	LOG_LEVEL=debug $(GO) run .

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests under the race detector
	$(GO) test -race -count=1 ./...

cover: ## Run tests and open a coverage report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1
	@echo "run: go tool cover -html=coverage.out"

bench: ## Run benchmarks
	$(GO) test -bench=. -benchmem ./...

lint: fmt vet ## Check formatting and run go vet

fmt: ## Report files that gofmt would change
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi
	@echo "formatting ok"

vet: ## Run go vet
	$(GO) vet ./...

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

deps: ## Download module dependencies
	$(GO) mod download

clean: ## Remove build output and coverage files
	rm -rf $(BUILD_DIR) coverage.out
	@echo "cleaned"

reset-db: ## Delete the database file and its WAL sidecars
	rm -f $(DB_PATH) $(DB_PATH)-wal $(DB_PATH)-shm
	@echo "removed $(DB_PATH)"

db: ## Open the database in the sqlite3 shell
	@command -v sqlite3 >/dev/null || { echo "sqlite3 is not installed"; exit 1; }
	sqlite3 $(DB_PATH)

docker: ## Build the container image
	docker build -t $(BINARY):latest .
