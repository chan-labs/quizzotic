#!/usr/bin/env bash
# Local dev stack: fake OIDC issuer + quizzotic server, seeded with test data.
# Usage: dev/dev.sh [all|issuer|server|reset]   (run from the repo root; see
# the `dev*` tasks in mise.toml). DEV ONLY: nothing here is for production.
#
# Environment:
#   QUIZZOTIC_DEV_PORT         server port (default 8089)
#   QUIZZOTIC_DEV_ISSUER_PORT  fake issuer port (default 8099)
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${QUIZZOTIC_DEV_PORT:-8089}"
ISSUER_PORT="${QUIZZOTIC_DEV_ISSUER_PORT:-8099}"
STATE=.dev
CONFIG=dev/quizzotic.dev.yaml

mkdir -p "$STATE/bin"

# Port overrides for the committed dev config (env beats the file).
export QUIZZOTIC_SERVER_LISTEN="127.0.0.1:${PORT}"
export QUIZZOTIC_SERVER_PUBLIC_URL="http://localhost:${PORT}"
export QUIZZOTIC_AUTH_ISSUERS_0_ISSUER="http://127.0.0.1:${ISSUER_PORT}"

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
	go build -o "$STATE/bin/devissuer" ./tools/devissuer
}

run_issuer() { DEVISSUER_ADDR="127.0.0.1:${ISSUER_PORT}" exec "$STATE/bin/devissuer"; }
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
	rm -f "$STATE"/quizzotic.db "$STATE"/quizzotic.db-shm "$STATE"/quizzotic.db-wal
	echo "dev: removed $STATE/quizzotic.db"
	;;
all)
	build
	pids=()
	trap 'kill "${pids[@]}" 2>/dev/null || true; wait 2>/dev/null || true' EXIT
	trap 'exit 130' INT TERM
	(run_issuer) &
	pids+=($!)
	wait_for "http://127.0.0.1:${ISSUER_PORT}/healthz"
	(run_server) &
	pids+=($!)
	wait_for "http://localhost:${PORT}/healthz"
	bootstrap
	echo
	echo "dev: ready"
	echo "  web UI:  http://localhost:${PORT}/   (sign-in is automatic: fake issuer, user dev@example.com)"
	echo "  issuer:  http://127.0.0.1:${ISSUER_PORT}/.well-known/openid-configuration"
	echo "  state:   ${STATE}/ (reset with: mise run dev-reset)"
	wait "${pids[@]}"
	;;
*)
	echo "usage: $0 [all|issuer|server|reset]" >&2
	exit 2
	;;
esac
