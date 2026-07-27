# Exposes the service as a gRPC server, against a database whose schema may be out of date.
#
# It ships the migrations binary alongside the server and applies pending migrations on start,
# which makes it larger than the base gRPC image and suited to local development.
FROM docker.io/library/golang:1.26.5-alpine AS builder

ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# grpcurl backs the healthcheck. Installed before the source COPY so its cached compile
# survives a source-only rebuild, and pinned for reproducibility.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/usr/local/bin go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3

COPY ./cmd/grpc ./cmd/grpc
COPY ./cmd/migrations ./cmd/migrations
COPY ./internal/handlers ./internal/handlers
COPY ./internal/dao ./internal/dao
COPY ./internal/core ./internal/core
COPY ./internal/models ./internal/models
COPY ./internal/config ./internal/config

# One RUN so the two binaries share a single warm module + build cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -trimpath -o /grpc ./cmd/grpc/ && \
    go build -ldflags="-s -w" -trimpath -o /migrations ./cmd/migrations/

FROM docker.io/library/alpine:3.24.1

WORKDIR /

COPY --from=builder /grpc /grpc
COPY --from=builder /migrations /migrations
COPY --from=builder /usr/local/bin/grpcurl /usr/local/bin/grpcurl

HEALTHCHECK --interval=1s --timeout=5s --retries=10 --start-period=1s \
  CMD grpcurl --plaintext -d '' localhost:8080 grpc.health.v1.Health/Check || exit 1

ENV GRPC_PORT=8080

# gRPC port.
EXPOSE 8080
# TLS port.
EXPOSE 443

# Apply pending migrations, then start the server.
CMD ["sh", "-c", "/migrations && /grpc"]
