# syntax=docker/dockerfile:1
# One image, one static binary with the web app inside. Both build stages run on the
# build machine's own architecture and cross-compile, so producing the arm64 image
# for oute-server needs no emulation.

FROM --platform=$BUILDPLATFORM node:24-trixie-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/oute-draw ./cmd/oute-draw

# No shell, no package manager, non-root: the binary is all that runs.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oute-draw /oute-draw
ENV DRAW_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["/oute-draw"]
