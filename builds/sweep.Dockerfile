# Runs the GenAI sweep server, which checks the generations nobody polled every SWEEP_INTERVAL.
# Requires a database with migrations already applied.
FROM docker.io/library/golang:1.27.4-alpine AS builder

# CGO_ENABLED=0 produces a fully static binary, required to run safely on Alpine's musl libc.
ENV CGO_ENABLED=0

WORKDIR /app

# Download dependencies before the source copy, so the module layer survives source-only rebuilds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# The whole source tree, so a new package needs no line here: .dockerignore drops the tests, and
# the build cache recompiles only what changed.
COPY ./cmd ./cmd
COPY ./internal ./internal

# -ldflags="-s -w" strips the symbol table and DWARF debug info, shrinking the binary.
# -trimpath removes local filesystem paths for reproducible builds.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -trimpath -o /sweep ./cmd/sweep/

FROM docker.io/library/alpine:3.24.4

COPY --from=builder /sweep /sweep

# Alpine ships BusyBox wget, so the healthcheck needs no extra package.
HEALTHCHECK --interval=1s --timeout=5s --retries=10 --start-period=1s \
  CMD ["wget", "-qO", "/dev/null", "http://localhost:8080/ping"]

ENV REST_PORT=8080

EXPOSE 8080

CMD ["/sweep"]
