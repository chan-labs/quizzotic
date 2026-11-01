VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint golden openapi-lint release-check snapshot

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/quizzotic ./cmd/quizzotic

test:
	go test -race ./...

lint:
	go vet ./...
	staticcheck ./...

# Regenerate the CLI help and MCP tool-list golden files after an intentional
# change to api/openapi.yaml or the generators. Review the diff before committing.
golden:
	go test ./internal/cli ./internal/mcp ./cmd/quizzotic -run Golden -update

# Pinned so local and CI results match. Needs Node.js (npx).
openapi-lint:
	npx --yes @redocly/cli@1.34.5 lint api/openapi.yaml

GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@v2.12.0

# Validate the GoReleaser config. Publishes nothing.
release-check:
	$(GORELEASER) check

# Local build of archives, checksums and images. Publishes nothing.
snapshot:
	$(GORELEASER) release --snapshot --clean --skip=publish
