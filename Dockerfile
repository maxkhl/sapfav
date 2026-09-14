# syntax=docker/dockerfile:1

# Build läuft immer auf der Plattform des Runners; Go kompiliert quer für das
# Ziel. Die Laufzeit-Stage enthält kein RUN, deshalb braucht buildx kein QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/sapfav ./cmd/sapfav \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/sapfav /sapfav
COPY --from=build --chown=65532:65532 /out/data /data

ENV SAPFAV_DB=/data/tcodes.db \
    SAPFAV_PORT=8000
EXPOSE 8000
VOLUME /data

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/sapfav", "healthcheck"]

ENTRYPOINT ["/sapfav"]
