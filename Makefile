.PHONY: build run test test-ci test-coverage lint vet fmt fmt-check tidy-check ci clean mocks

APP_NAME := flowgate
BUILD_DIR := bin
MAIN_PKG := ./cmd

# --- Tools ---

GOLANGCI_LINT_VERSION := v2.11.4
GOLANGCI_LINT := $(BUILD_DIR)/golangci-lint

GOIMPORTS_VERSION := v0.44.0
GOIMPORTS := $(BUILD_DIR)/goimports

MOCKERY_VERSION := v2.53.3
MOCKERY := $(BUILD_DIR)/mockery

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/$(BUILD_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOIMPORTS):
	GOBIN=$(CURDIR)/$(BUILD_DIR) go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

$(MOCKERY):
	GOBIN=$(CURDIR)/$(BUILD_DIR) go install github.com/vektra/mockery/v2@$(MOCKERY_VERSION)

# --- Commands ---

build:
	go build -trimpath -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PKG)

run: build
	./$(BUILD_DIR)/$(APP_NAME)

test:
	go test -race -cover -coverprofile=coverage.out ./...

test-ci:
	go test -race -shuffle=on -timeout=5m -covermode=atomic -coverprofile=coverage.out ./...

test-coverage: test
	go tool cover -func=coverage.out

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

vet:
	go vet ./...

fmt: $(GOIMPORTS)
	gofmt -w .
	$(GOIMPORTS) -w .

fmt-check: $(GOIMPORTS)
	@diff=$$(gofmt -l . ; $(GOIMPORTS) -l .); \
	 if [ -n "$$diff" ]; then \
	   echo "Unformatted files:"; echo "$$diff"; exit 1; \
	 fi

tidy-check:
	@if ! go mod tidy -diff; then \
	   echo "go.mod/go.sum are not tidy. Run 'go mod tidy' and commit the result."; \
	   exit 1; \
	 fi

mocks: $(MOCKERY)
	$(MOCKERY)

ci: tidy-check fmt-check vet lint test-ci build

clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.txt
