# Use the official Golang image as a build stage
FROM docker.io/library/golang:latest AS builder

# Install dependencies needed for GPGME
RUN apt-get update && apt-get upgrade -y && apt-get install -y \
    pkg-config \
    libgpgme-dev \
    gcc \
    make

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Download the dependencies
RUN go mod download

COPY . .

# Build the Go app
RUN go build -o /app/sync_registries

# Command to run the Go program
CMD ["/app/sync_registries"]
