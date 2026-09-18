# syntax=docker/dockerfile:1.7
FROM golang:1.25.14-trixie AS build

RUN apt-get update \
    && apt-get dist-upgrade -y \
    && apt-get install -y --no-install-recommends \
       gcc g++ flex bison pkg-config meson ninja-build python3 ca-certificates \
       libglib2.0-dev liborc-0.4-dev zlib1g-dev libpng-dev libx264-dev \
       libopenh264-dev libfaad-dev libvo-aacenc-dev librtmp-dev libsrt-gnutls-dev \
    && rm -rf /var/lib/apt/lists/*
COPY scripts/media-sources.json /media-sources.json
COPY scripts/build-media.py /build-media.py
ENV PKG_CONFIG_PATH=/opt/streamtool-media/lib/pkgconfig \
    CGO_LDFLAGS=-L/opt/streamtool-media/lib \
    LD_LIBRARY_PATH=/opt/streamtool-media/lib \
    GST_PLUGIN_SYSTEM_PATH=/opt/streamtool-media/lib/gstreamer-1.0
RUN python3 /build-media.py
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILT_AT=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -tags gstreamer \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.builtAt=${BUILT_AT}" \
    -o /out/stream-worker ./cmd/stream-worker
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X streamtool-relay/internal/buildinfo.Version=${VERSION}" -o /out/api ./cmd/api

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-X streamtool-relay/internal/buildinfo.Version=${VERSION}" -o /out/node-agent ./cmd/node-agent
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate

FROM debian:13.1-slim AS media-runtime
RUN apt-get update \
    && apt-get dist-upgrade -y \
    && apt-get install -y --no-install-recommends \
       ca-certificates gstreamer1.0-tools gstreamer1.0-plugins-base \
       gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
       gstreamer1.0-plugins-ugly gstreamer1.0-libav \
    && rm -rf /var/lib/apt/lists/*

FROM media-runtime AS fixture-publisher
USER 65532:65532
ENTRYPOINT ["gst-launch-1.0", "-e"]

FROM build AS minimal-media-build
COPY scripts/minimal-media-runtime.py /minimal-media-runtime.py
RUN python3 /minimal-media-runtime.py

FROM scratch AS stream-worker
# Exact ELF closure, startup shell/sleep and certificates, with package inventory
# and licenses retained. No base-image utilities or package manager are shipped.
COPY --from=minimal-media-build /minimal-media/ /
COPY assets/offline.png /usr/local/share/streamtool/offline.png
COPY --chmod=755 scripts/managed-worker.sh /usr/local/bin/managed-worker
ENV XDG_CACHE_HOME=/tmp \
    LD_LIBRARY_PATH=/opt/streamtool-media/lib \
    GST_PLUGIN_SYSTEM_PATH=/opt/streamtool-media/lib/gstreamer-1.0
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/stream-worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS control-api
COPY --chown=65532:65532 assets/offline.png /data/.volume-owner
COPY --from=build /out/api /usr/local/bin/api
COPY sqlite-migrations /sqlite-migrations
ENTRYPOINT ["/usr/local/bin/api"]

FROM docker:29.8.1-cli AS node-agent
RUN apk upgrade --no-cache && rm -rf /usr/local/libexec/docker/cli-plugins
COPY --from=build /out/node-agent /usr/local/bin/node-agent
ENTRYPOINT ["/usr/local/bin/node-agent"]

FROM gcr.io/distroless/static-debian12:nonroot AS migration-job
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY sqlite-migrations /sqlite-migrations
ENTRYPOINT ["/usr/local/bin/migrate"]

# Include the production build-tag path (native pipeline and Go bindings), not
# just the non-GStreamer stub used by portable unit tests.
FROM build AS security-source
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOTOOLCHAIN=go1.26.8 go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -tags gstreamer ./...
