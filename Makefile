# Transitional shim: tasks now live in mise.toml. Each target just runs the
# matching `mise run` task so existing `make test lint` habits keep working.
# This file exists for the transition and can be removed.

.PHONY: build test lint golden openapi-lint release-check snapshot test-postgres build-all ci

build test lint golden openapi-lint release-check snapshot test-postgres build-all ci:
	mise run $@
