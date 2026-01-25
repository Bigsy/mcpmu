.PHONY: build test test-v test-integration lint clean

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
