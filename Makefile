VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint openapi-lint

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/quizzotic ./cmd/quizzotic

test:
	go test -race ./...

lint:
	go vet ./...
	staticcheck ./...

# Pinned so local and CI results match. Needs Node.js (npx).
openapi-lint:
	npx --yes @redocly/cli@1.34.5 lint api/openapi.yaml
