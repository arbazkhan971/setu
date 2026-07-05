BINARY := setu
PKG := github.com/arbazkhan971/setu
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt run tidy clean install

build: ## Build the setu binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/setu

test: ## Run tests with race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go files
	gofmt -s -w .

run: build ## Build and run with config.yaml
	./$(BINARY) --config config.yaml

tidy: ## Tidy modules
	go mod tidy

install: ## Install the binary to GOPATH/bin
	go install -ldflags "$(LDFLAGS)" ./cmd/setu

clean: ## Remove build artifacts
	rm -f $(BINARY)
	go clean
