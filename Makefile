.PHONY: build install test test-v test-integration test-all lint fmt fmt-check fix check clean run debug

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

# ./... rather than an enumerated package list: the old list silently missed
# every package added since it was written (metrics, httpserve, ...).
lint:
	@mkdir -p /tmp/mcpmu-gocache /tmp/mcpmu-golangci
	GOCACHE=/tmp/mcpmu-gocache GOLANGCI_LINT_CACHE=/tmp/mcpmu-golangci golangci-lint run ./...

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
