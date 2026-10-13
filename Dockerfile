# Built by GoReleaser (dockers_v2), which puts each platform's binary at
# $TARGETPLATFORM/qh in the build context.
FROM busybox:1.37 AS data
RUN mkdir /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY --from=data --chown=65532:65532 /data /data
COPY $TARGETPLATFORM/qh /usr/bin/qh
# The SQLite store and master.key live in /data unless QH_STORE_DSN and
# QH_MASTER_KEY say otherwise.
ENV QH_HOME=/data
VOLUME /data
EXPOSE 8765
ENTRYPOINT ["/usr/bin/qh"]
CMD ["serve", "--http", ":8765"]
