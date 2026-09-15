BINARY_NAME=agent-sync
GO=go
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

.PHONY: all build clean test test-short lint lint-fix fmt vet run mod audit help

all: fmt lint test build

build:
	@echo "Building $(BINARY_NAME)..."
	$(GO) build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/agentsync

clean:
	rm -rf bin/ coverage.out coverage.html

test:
	$(GO) test -race ./...

test-short:
	$(GO) test -short ./...

lint:
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

fmt:
	golangci-lint fmt ./...

vet:
	$(GO) vet ./...

run: build
	./bin/$(BINARY_NAME)

mod:
	$(GO) mod tidy
	$(GO) mod vendor

audit:
	govulncheck ./...

help:
	@printf '  %-11s %s\n' \
		all 'Format, lint, test and build' \
		build 'Build the application' \
		clean 'Clean build artifacts' \
		test 'Run all tests with race detector' \
		test-short 'Run tests without long-running ones' \
		lint 'Run linter' \
		lint-fix 'Run linter and auto-fix issues' \
		fmt 'Format code' \
		vet 'Run go vet' \
		run 'Build and run the application' \
		mod 'Tidy modules and vendor dependencies' \
		audit 'Audit dependencies for known vulnerabilities' \
		help 'Show this help message'
