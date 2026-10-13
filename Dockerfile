# Built by GoReleaser (dockers_v2), which puts each platform's binary at
# $TARGETPLATFORM/qh in the build context.

# Runs on the build platform only, so no emulation is needed.
FROM --platform=$BUILDPLATFORM busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e AS data
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETPLATFORM
COPY --from=data --chown=65532:65532 /data /data
COPY $TARGETPLATFORM/qh /usr/bin/qh
# The SQLite store lives in /data unless QH_STORE_DSN is set; server mode
# takes its master key from QH_MASTER_KEY.
ENV QH_HOME=/data
VOLUME /data
EXPOSE 8765
ENTRYPOINT ["/usr/bin/qh"]
CMD ["serve", "--http", ":8765"]
