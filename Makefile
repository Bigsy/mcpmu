.PHONY: build test test-v test-integration lint clean run run-debug

build:
	go build -o mcp-studio ./cmd/mcp-studio

test:
	go test -race ./...

test-v:
	go test -race -v ./...

test-integration:
	go test -tags=integration -race ./...

lint:
	golangci-lint run

clean:
	rm -f mcp-studio
	go clean -testcache

run:
	go run ./cmd/mcp-studio

debug:
	go run ./cmd/mcp-studio -debug
