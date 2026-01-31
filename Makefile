.PHONY: build install test test-v test-integration lint clean run debug

build:
	go build -o mcpmu ./cmd/mcpmu

install: build
	mkdir -p ~/.local/bin
	cp mcpmu ~/.local/bin/mcpmu

test:
	go test -race ./...

test-v:
	go test -race -v ./...

test-integration:
	go test -tags=integration -race ./...

lint:
	golangci-lint run

clean:
	rm -f mcpmu
	go clean -testcache

run:
	go run ./cmd/mcpmu

debug:
	go run ./cmd/mcpmu --debug
