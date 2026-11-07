#!/usr/bin/env bash
# Local dev stack: fake OIDC issuer + quizzotic server, seeded with test data.
# Usage: dev/dev.sh [all|issuer|server|reset]   (run from the repo root; see
# the `dev*` tasks in mise.toml). DEV ONLY: nothing here is for production.
#
# Environment:
#   QUIZZOTIC_DEV_PORT         server port (default 8089)
#   QUIZZOTIC_DEV_OTEL         when non-empty, export traces to the local stack
#                              from `mise run dev-otel` (OTLP HTTP on :4318)
#   QUIZZOTIC_DEV_ISSUER_PORT  fake issuer port (default 8099); the database is
#                              .dev/quizzotic-<issuer port>.db
# Needs Docker: the issuer is navikt/mock-oauth2-server (config in
# dev/mock-oauth2.json), issuer URL http://localhost:<port>/dev.
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${QUIZZOTIC_DEV_PORT:-8089}"
ISSUER_PORT="${QUIZZOTIC_DEV_ISSUER_PORT:-8099}"
STATE=.dev
CONFIG=dev/quizzotic.dev.yaml
ISSUER_CONTAINER="quizzotic-dev-issuer-${ISSUER_PORT}"
# Pinned by tag and digest.
ISSUER_IMAGE="ghcr.io/navikt/mock-oauth2-server:2.1.10@sha256:65d4ed47ce094cc3849e41e03499bfb32372d69c726ec22b3d5a19eba9bc787e"

mkdir -p "$STATE/bin"

# Opt-in trace export to `mise run dev-otel`; explicit OTEL_* values win.
if [ -n "${QUIZZOTIC_DEV_OTEL:-}" ]; then
	export OTEL_EXPORTER_OTLP_ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-http://localhost:4318}"
	export OTEL_EXPORTER_OTLP_PROTOCOL="${OTEL_EXPORTER_OTLP_PROTOCOL:-http/protobuf}"
	export OTEL_SERVICE_NAME="${OTEL_SERVICE_NAME:-quizzotic-dev}"
fi

# Port overrides for the committed dev config (env beats the file).
export QUIZZOTIC_SERVER_LISTEN="127.0.0.1:${PORT}"
export QUIZZOTIC_SERVER_PUBLIC_URL="http://localhost:${PORT}"
export QUIZZOTIC_AUTH_ISSUERS_0_ISSUER="http://localhost:${ISSUER_PORT}/dev"
# A user is keyed by (issuer, subject) and the issuer URL carries the port, so
# a database signed into under another issuer port would hold a second user and
# `admin seed` ("the only user") would refuse. Keep one database per issuer
# port: changing the port starts a fresh, correctly seeded database.
DB="$STATE/quizzotic-${ISSUER_PORT}.db"
export QUIZZOTIC_STORAGE_URL="sqlite://${DB}"
wait_for() { # url
	for _ in $(seq 1 100); do
		curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
		sleep 0.2
	done
	echo "dev: timed out waiting for $1" >&2
	return 1
}

build() {
	go build -o "$STATE/bin/quizzotic" ./cmd/quizzotic
}

run_issuer() {
	command -v docker >/dev/null || { echo "dev: Docker is required for the fake issuer" >&2; exit 1; }
	docker rm -f "$ISSUER_CONTAINER" >/dev/null 2>&1 || true
	exec docker run --rm --init --name "$ISSUER_CONTAINER" \
		-p "127.0.0.1:${ISSUER_PORT}:8080" \
		-e SERVER_PORT=8080 -e JSON_CONFIG_PATH=/config/config.json \
		-v "$PWD/dev/mock-oauth2.json:/config/config.json:ro" \
		"$ISSUER_IMAGE"
}
stop_issuer() { docker rm -f "$ISSUER_CONTAINER" >/dev/null 2>&1 || true; }
run_server() { exec "$STATE/bin/quizzotic" serve --config "$CONFIG"; }

# The first sign-in creates the user; seed files need a user to own them, so
# sign in once with curl (as the browser would), then apply the seeds.
bootstrap() {
	local jar
	jar="$(mktemp)"
	curl -fsS -L -c "$jar" -b "$jar" -o /dev/null "http://localhost:${PORT}/auth/login" ||
		echo "dev: bootstrap sign-in failed; sign in with a browser, then restart" >&2
	rm -f "$jar"
	for s in builtin:fsrs-basics dev/seed.yaml; do
		"$STATE/bin/quizzotic" admin seed "$s" --config "$CONFIG" >/dev/null ||
			echo "dev: seeding $s failed" >&2
	done
}

case "${1:-all}" in
issuer)
	build
	run_issuer
	;;
server)
	build
	run_server
	;;
reset)
	rm -f "$STATE"/quizzotic*.db "$STATE"/quizzotic*.db-shm "$STATE"/quizzotic*.db-wal
	echo "dev: removed $STATE/quizzotic*.db"
	;;
all)
	build
	pids=()
	trap 'kill "${pids[@]}" 2>/dev/null || true; stop_issuer; wait 2>/dev/null || true' EXIT
	trap 'exit 130' INT TERM
	(run_issuer) &
	pids+=($!)
	wait_for "http://localhost:${ISSUER_PORT}/dev/.well-known/openid-configuration"
	(run_server) &
	pids+=($!)
	wait_for "http://localhost:${PORT}/healthz"
	bootstrap
	echo
	echo "dev: ready"
	echo "  web UI:  http://localhost:${PORT}/   (sign-in is automatic: fake issuer, user dev@example.com)"
	echo "  issuer:  http://localhost:${ISSUER_PORT}/dev/.well-known/openid-configuration"
	echo "  state:   ${STATE}/ (reset with: mise run dev-reset)"
	wait "${pids[@]}"
	;;
*)
	echo "usage: $0 [all|issuer|server|reset]" >&2
	exit 2
	;;
esac
