# Use Alpine as the base image for building
FROM alpine:latest AS builder

# Install Go and dependencies needed for GPGME and building
RUN apk update && apk upgrade && apk add --no-cache \
    go \
    git \
    gcc \
    musl-dev \
    gpgme-dev \
    pkgconfig \
    make

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Download the dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the Go app
RUN go build -o /app/sync_registries

# Final image stage: use a smaller Alpine base image to run the Go app
FROM alpine:latest

# Install runtime dependencies
RUN apk update && apk upgrade && apk add --no-cache gpgme

# Copy the Go binary from the builder stage
COPY --from=builder /app/sync_registries /app/sync_registries

# Command to run the Go program
CMD ["/app/sync_registries"]

