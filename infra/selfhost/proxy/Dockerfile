# Standard Caddy, pinned with the current security fixes in transitive Go modules.
FROM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY main.go ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/caddy .
RUN --mount=type=cache,target=/go/pkg/mod cp /go/pkg/mod/github.com/caddyserver/caddy/v2@v2.11.4/LICENSE /out/LICENSE
FROM build AS security-source
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

FROM caddy:2.11.4-alpine
RUN apk upgrade --no-cache
COPY --from=build /out/caddy /usr/bin/caddy
COPY --from=build /out/LICENSE /usr/share/licenses/caddy/LICENSE
