# Use a pinned Alpine version for reproducible builds
FROM alpine:3.21 AS builder

# Install Go and dependencies needed for GPGME and building
RUN apk update && apk upgrade && apk add --no-cache \
    go \
    git \
    gcc \
    musl-dev \
    gpgme-dev \
    pkgconfig \
    make

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a statically linked-friendly binary with CGO for gpgme
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /app/sync_registries .

FROM alpine:3.21

RUN apk update && apk upgrade && apk add --no-cache gpgme ca-certificates \
    && adduser -D -H -u 65532 syncer

COPY --from=builder /app/sync_registries /app/sync_registries

USER 65532:65532

CMD ["/app/sync_registries"]
