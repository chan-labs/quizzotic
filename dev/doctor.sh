#!/usr/bin/env bash
# Check this machine for what Quizzotic development and agent sessions need.
# Usage: mise run doctor (run from the repo root; reads only, changes nothing).
#
# Prints one line per check: OK, MISSING or WARN, and for anything missing,
# what it disables and the workaround. Exits non-zero only when a tool that
# `mise run test ::: lint` needs (go, golangci-lint, node) is missing.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

required_missing=0

ok() { printf '  OK       %-16s %s\n' "$1" "$2"; }
missing() { printf '  MISSING  %-16s %s\n' "$1" "$2"; }
warn() { printf '  WARN     %-16s %s\n' "$1" "$2"; }

# version prints the first line of "<cmd> <args>" or nothing.
version() { "$@" 2>/dev/null | head -n1; }

echo "Tools (pinned in mise.toml; run 'mise install' if any are missing):"
if command -v go >/dev/null 2>&1; then
	ok go "$(version go version)"
else
	missing go "mise install; nothing builds or tests without it"
	required_missing=1
fi
if command -v golangci-lint >/dev/null 2>&1; then
	ok golangci-lint "$(version golangci-lint version --short)"
else
	missing golangci-lint "mise install; 'mise run lint' can't run"
	required_missing=1
fi
if command -v node >/dev/null 2>&1; then
	ok node "$(version node --version)"
else
	missing node "mise install; openapi-lint and the internal/web JS test can't run"
	required_missing=1
fi
if command -v govulncheck >/dev/null 2>&1; then
	ok govulncheck "$(version govulncheck -version | sed -n 's/^Scanner: //p')"
else
	missing govulncheck "mise install; 'mise run vulncheck' can't run (CI still runs it)"
fi

if command -v goreleaser >/dev/null 2>&1; then
	ok goreleaser "$(goreleaser --version 2>/dev/null | sed -n 's/^GitVersion: *//p')"
else
	missing goreleaser "mise install; 'mise run release-check' can't run (CI still runs it)"
fi

echo
echo "GitHub CLI:"
if command -v gh >/dev/null 2>&1; then
	ok gh "$(version gh --version) ($(command -v gh))"
	if status="$(gh auth status 2>&1)"; then
		ok "gh auth" "logged in"
		if grep -q "'workflow'" <<<"$status"; then
			ok "gh scopes" "token has the workflow scope"
		else
			warn "gh scopes" "no workflow scope (check 'gh auth status'): pushes that change .github/workflows/ are refused; fix with 'gh auth refresh -h github.com -s workflow'"
		fi
	else
		missing "gh auth" "gh auth login; PRs, CI checks and merges can't be done from here"
	fi
else
	missing gh "mise install (gh is pinned in mise.toml); PRs, CI checks and merges can't be done from here"
fi

echo
echo "Docker:"
if ! command -v docker >/dev/null 2>&1; then
	missing docker "test-postgres, snapshot and dev-otel can't run; rely on CI's 'test (postgres)' job (dev falls back to tools/devissuer)"
elif ! docker info >/dev/null 2>&1; then
	missing "docker daemon" "docker is installed but not running; test-postgres, snapshot and dev-otel can't run"
else
	ok docker "$(version docker --version)"
fi

echo
echo "Git:"
name="$(git config --get user.name || true)"
email="$(git config --get user.email || true)"
if [ -n "$name" ] && [ -n "$email" ]; then
	ok identity "$name <$email>"
else
	missing identity "git commit fails with 'Author identity unknown'; commit with 'git -c user.name=... -c user.email=... commit'"
fi
if [ "$(git config --get commit.gpgsign || true)" = "true" ]; then
	ok signing "commit.gpgsign=true ($(git config --get gpg.format || echo openpgp))"
else
	warn signing "commits are not signed (commit.gpgsign is not true); fine unless main requires signed commits"
fi
if git ls-remote --exit-code origin HEAD >/dev/null 2>&1; then
	ok "origin read" "$(git remote get-url origin)"
else
	missing "origin read" "can't reach origin; fetch, push and PRs won't work"
fi
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
	perm="$(gh repo view --json viewerPermission -q .viewerPermission 2>/dev/null || true)"
	case "$perm" in
	ADMIN | MAINTAIN | WRITE) ok "push access" "gh reports $perm on the repository" ;;
	*) missing "push access" "gh reports '${perm:-unknown}' permission; branches can't be pushed" ;;
	esac
else
	warn "push access" "not checked (needs gh logged in)"
fi

echo
if [ "$required_missing" -ne 0 ]; then
	echo "doctor: required tools are missing; run 'mise install'." >&2
	exit 1
fi
echo "doctor: required tools present (see MISSING/WARN lines for what's disabled)."
