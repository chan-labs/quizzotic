# The runtime user is numeric (distroless nonroot = 65532) so Kubernetes
# runAsNonRoot can verify it without a runAsUser override.
# Used by GoReleaser: the prebuilt static binary is placed at the root of the
# build context. For a local build, run `mise run snapshot`.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/quizzotic /usr/local/bin/quizzotic
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/quizzotic"]
CMD ["serve", "--config", "/etc/quizzotic/quizzotic.yaml"]
