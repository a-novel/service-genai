# Standalone gRPC server image for local development. Applies migrations automatically before
# starting the server, so the database needs no pre-initialization.
# Production deployments use the base gRPC image (grpc.Dockerfile).
FROM docker.io/library/golang:1.27.3-alpine AS builder

ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/usr/local/bin go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3

COPY ./cmd/grpc ./cmd/grpc
COPY ./cmd/migrations ./cmd/migrations
COPY ./internal/handlers ./internal/handlers
COPY ./internal/dao ./internal/dao
COPY ./internal/lib ./internal/lib
COPY ./internal/core ./internal/core
COPY ./internal/models ./internal/models
COPY ./internal/config ./internal/config

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -trimpath -o /grpc ./cmd/grpc/ && \
    go build -ldflags="-s -w" -trimpath -o /migrations ./cmd/migrations/

FROM docker.io/library/alpine:3.24.3

COPY --from=builder /grpc /grpc
COPY --from=builder /migrations /migrations
COPY --from=builder /usr/local/bin/grpcurl /usr/local/bin/grpcurl

HEALTHCHECK --interval=1s --timeout=5s --retries=10 --start-period=1s \
  CMD ["grpcurl", "--plaintext", "-d", "", "localhost:8080", "grpc.health.v1.Health/Check"]

ENV GRPC_PORT=8080

EXPOSE 8080
# TLS port.
EXPOSE 443

CMD ["sh", "-c", "/migrations && /grpc"]
