# Single-node MinIO with the web console. Built from the upstream .deb's static
# binary on Alpine because MinIO's official image dropped the bundled console.
#
# `minio` (the extracted binary, ~118 MB) is NOT in git: `make container-build-minio`
# pulls it out of the .deb right before building. It is COPY'd from this build
# context (embed/). Runtime is pull-only from the public ghcr tag; pgcli never
# builds this image.
FROM docker.io/library/alpine:3.22

RUN apk add --no-cache ca-certificates

COPY minio /usr/local/bin/minio

EXPOSE 9000 9001

# pgcli supplies `server /data --address ... --console-address ...` at run time.
ENTRYPOINT ["/usr/local/bin/minio"]
