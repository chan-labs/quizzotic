# Releasing

Releases are cut by pushing a semver tag. The `Release` workflow
(`.github/workflows/release.yml`) runs only on tags matching `v*`, never on
branches or pull requests. It runs GoReleaser, which:

- builds static binaries (`CGO_ENABLED=0`) for linux, darwin and windows on amd64 and arm64;
- publishes archives, `checksums.txt` and a changelog as a GitHub release;
- builds and pushes a multi-arch (amd64, arm64) distroless image to `ghcr.io/chan-labs/quizzotic`, tagged `<version>` and `latest`.

## Cutting a release

1. Move the `[Unreleased]` entries in `CHANGELOG.md` under a new version heading and merge to `main`.
2. Tag and push: `git tag -s v0.1.0 -m v0.1.0 && git push origin v0.1.0`.
3. Watch the `Release` workflow. Tags with a pre-release suffix (`v0.1.0-rc.1`) are marked as pre-releases and do not move `latest`.
4. First release only: the GHCR package is created private. In the repository's package settings, change its visibility to public and link it to the repository, then check `docker pull ghcr.io/chan-labs/quizzotic:<version>` works while logged out.

## Trying it locally

Nothing below publishes anything.

```sh
mise run release-check   # validate .goreleaser.yaml
mise run snapshot        # build archives, checksums and local images into ./dist
```

Both run GoReleaser through `go run`, so no separate install is needed;
`mise run snapshot` also needs Docker with buildx for the image step.

## Running the image

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/quizzotic.yaml:/etc/quizzotic/quizzotic.yaml:ro" \
  ghcr.io/chan-labs/quizzotic:latest
```
