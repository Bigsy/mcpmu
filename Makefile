.PHONY: build install test test-v test-integration test-all lint fmt fmt-check fix check clean run debug

LINT_DIRS := \
	cmd/mcpmu \
	internal/config \
	internal/mcp \
	internal/oauth \
	internal/process \
	internal/registry \
	internal/server \
	internal/tui \
	internal/tui/views \
	internal/web

build:
	go build -o mcpmu ./cmd/mcpmu

install: build
	mkdir -p ~/.local/bin
	cp mcpmu ~/.local/bin/mcpmu
	codesign --force --sign - ~/.local/bin/mcpmu

test:
	go test -race ./...

test-v:
	go test -race -v ./...

test-integration:
	go test -tags=integration -race ./...

test-all:
	go test -tags=integration -race -timeout=5m ./...

lint:
	@mkdir -p /tmp/mcpmu-gocache /tmp/mcpmu-golangci
	@for dir in $(LINT_DIRS); do \
		echo "golangci-lint $$dir"; \
		GOCACHE=/tmp/mcpmu-gocache GOLANGCI_LINT_CACHE=/tmp/mcpmu-golangci golangci-lint run $$dir || exit $$?; \
	done

fix:
	go fix ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

check: fix fmt-check lint test

clean:
	rm -f mcpmu
	go clean -testcache
run:
	go run ./cmd/mcpmu

debug:
	go run ./cmd/mcpmu --debug
