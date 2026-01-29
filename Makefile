.PHONY: build install test test-v test-integration lint clean run run-debug

build:
	go build -o mcp-go ./cmd/mcp-studio

install: build
	mkdir -p ~/.local/bin
	cp mcp-go ~/.local/bin/mcp-go

test:
	go test -race ./...

test-v:
	go test -race -v ./...

test-integration:
	go test -tags=integration -race ./...

lint:
	golangci-lint run

clean:
	rm -f mcp-go
	go clean -testcache

run:
	go run ./cmd/mcp-studio

debug:
	go run ./cmd/mcp-studio -debug
