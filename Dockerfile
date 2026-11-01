# Used by GoReleaser: the prebuilt static binary is placed at the root of the
# build context. For a local build, run `make snapshot`.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/quizzotic /usr/local/bin/quizzotic
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/quizzotic"]
CMD ["serve", "--config", "/etc/quizzotic/quizzotic.yaml"]
