# Runs the GenAI REST server that providers post their notifications to. Requires a database with
# migrations already applied.
FROM docker.io/library/golang:1.27.3-alpine AS builder

# CGO_ENABLED=0 produces a fully static binary, required to run safely on Alpine's musl libc.
ENV CGO_ENABLED=0

WORKDIR /app

# Download dependencies before the source copy, so the module layer survives source-only rebuilds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY ./cmd/rest ./cmd/rest
COPY ./internal/handlers ./internal/handlers
COPY ./internal/dao ./internal/dao
COPY ./internal/lib ./internal/lib
COPY ./internal/core ./internal/core
COPY ./internal/models ./internal/models
COPY ./internal/config ./internal/config

# -ldflags="-s -w" strips the symbol table and DWARF debug info, shrinking the binary.
# -trimpath removes local filesystem paths for reproducible builds.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -trimpath -o /rest ./cmd/rest/

FROM docker.io/library/alpine:3.24.2

COPY --from=builder /rest /rest

# Alpine ships BusyBox wget, so the healthcheck needs no extra package.
HEALTHCHECK --interval=1s --timeout=5s --retries=10 --start-period=1s \
  CMD ["wget", "-qO", "/dev/null", "http://localhost:8080/ping"]

ENV REST_PORT=8080

EXPOSE 8080

CMD ["/rest"]
