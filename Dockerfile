# syntax=docker/dockerfile:1
#
# Single runtime image: the hub serves the embedded web UI, the REST API, and
# the WebSocket endpoints. The optional Traefik ingress (compose profile
# `traefik`) routes to it via container labels.
#
# Node daemons are NOT containerized by design: they run locally on each target
# machine (where the agent CLIs live) and dial out to the hub. See
# docs/DEPLOYMENT.md.

# Build stages run on the build host and cross-compile the Go binaries to
# $TARGETARCH, so multi-arch builds (linux/amd64 + linux/arm64) need no
# emulation. TARGETOS/TARGETARCH are empty under a non-buildx builder, in which
# case Go builds natively for the runtime platform.
FROM --platform=$BUILDPLATFORM node:22-alpine AS build-web
WORKDIR /src
RUN npm install -g pnpm@11.25.0
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
# VITE_HUB_URL is only for split hosting (SPA on one origin, hub on another);
# the default same-origin behavior needs no build-time configuration.
ARG VITE_HUB_URL=/
RUN cd web && VITE_HUB_URL=$VITE_HUB_URL pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-go
ARG TARGETOS
ARG TARGETARCH
# Pinned migration runner (same version as the dev toolchain). Cross-compiled
# for the runtime platform so the copied binary matches the final stage.
# Installed before the source copy so source changes never invalidate this layer.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the web UI into the hub binary (internal/hub/static is the embed root).
COPY --from=build-web /src/web/dist/ internal/hub/static/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/hub ./cmd/hub \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/node ./cmd/node \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/fleetctl ./cmd/fleetctl

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build-go /out/hub /out/node /out/fleetctl /usr/local/bin/
COPY --from=build-go /go/bin/goose /usr/local/bin/goose
COPY migrations/ /app/migrations/
WORKDIR /app
CMD ["/usr/local/bin/hub"]
