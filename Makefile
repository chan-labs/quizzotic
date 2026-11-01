VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/quizzotic ./cmd/quizzotic

test:
	go test -race ./...

lint:
	go vet ./...
	staticcheck ./...
