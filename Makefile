.PHONY: build install test test-v test-integration lint fmt fmt-check check clean run debug

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

lint:
	golangci-lint run

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

check: fmt-check lint test

clean:
	rm -f mcpmu
	go clean -testcache

run:
	go run ./cmd/mcpmu

debug:
	go run ./cmd/mcpmu --debug
