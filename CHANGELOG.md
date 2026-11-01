# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Open-source project files: LICENSE (Apache-2.0), NOTICE, README, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT, CHANGELOG.
- `docs/dependencies.md` listing the allowed third-party dependencies, and `docs/adr/` for architecture decision records.
- GoReleaser configuration, release workflow (tag-triggered) and public multi-arch distroless image on GHCR; see `docs/releasing.md`.
- `deploy/` Docker Compose example running the published image with Pocket ID and Caddy, with a sample `quizzotic.yaml`.
