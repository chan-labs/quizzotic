# Releasing

Releases are cut by creating a new GitHub Release with a new tag (in the GitHub Releases UI, or with `gh release create`) and letting the `Release` workflow generate the artifacts. Prefer creating the tag through a GitHub Release; a hand-pushed `v*` tag also triggers the workflow.

The workflow (`.github/workflows/release.yml`) is triggered by the push of a tag matching `v*`, never by branches or pull requests (it does not listen for the `release` event). Publishing a release with a new tag creates that tag in the repository, and that tag push starts the workflow. It runs GoReleaser, which:

- builds static binaries (`CGO_ENABLED=0`) for linux, darwin and windows on amd64 and arm64;
- publishes archives, `checksums.txt` and a changelog as a GitHub release;
- builds and pushes a multi-arch (amd64, arm64) distroless image to `ghcr.io/chan-labs/quizzotic`, tagged `<version>` and `latest`.

## Cutting a release

1. Move the `[Unreleased]` entries in `CHANGELOG.md` under a new version heading and merge to `main`.
2. Create the release from `main` with a new tag: in the GitHub UI choose "Draft a new release", "Choose a tag", type a new tag such as `v0.3.0` and publish; or run `gh release create v0.3.0 --target main --title v0.3.0 --generate-notes`. Tags are `v` plus a semver version, for example `v0.3.0`; the older `v0.1` predates this convention.
3. Watch the `Release` workflow. It uploads the archives and `checksums.txt` to the release and pushes the image. Tags with a pre-release suffix (`v0.3.0-rc.1`) are marked as pre-releases and do not move `latest`.
4. First release only: the GHCR package is created private. In the repository's package settings, change its visibility to public and link it to the repository, then check `docker pull ghcr.io/chan-labs/quizzotic:<version>` works while logged out.

Image tags have no leading `v`: git tag `v0.2.0` publishes `ghcr.io/chan-labs/quizzotic:0.2.0` (and `latest`).

Notes on a release created in the UI. The tag does not exist until the release is published, so saving it as a draft starts nothing. GoReleaser finds the release for the tag and fills it in; its default `keep-existing` mode leaves any notes you wrote in place.

## Trying it locally

Nothing below publishes anything.

```sh
mise run release-check   # validate .goreleaser.yaml
mise run snapshot        # build archives, checksums and local images into ./dist
```

Both run GoReleaser through `go run`, so no separate install is needed;
`mise run snapshot` also needs Docker with buildx for the image step.

## Running the image

The image runs as the numeric user `65532:65532` (distroless `nonroot`), so it starts under Kubernetes `runAsNonRoot: true` with no `runAsUser` override. A mounted data volume must be writable by uid/gid 65532 (for example `fsGroup: 65532`).

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/quizzotic.yaml:/etc/quizzotic/quizzotic.yaml:ro" \
  ghcr.io/chan-labs/quizzotic:latest
```
