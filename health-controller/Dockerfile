# Multi-stage build: First stage - Build the Go application
# Use a valid Go version and specify platform
FROM --platform=linux/amd64 golang:1.24.3 AS builder

# Install ca-certificates in the builder stage
RUN apt-get update && apt-get install -y git ca-certificates

# Set the working directory inside the container
WORKDIR /app

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download Go module dependencies
RUN go mod download

# Copy all source files
COPY . .

# Build the Go application with static linking
# CGO_ENABLED=0: Disable CGO for static binary
# GOOS=linux: Target Linux operating system
# GOARCH=amd64: Target AMD64 architecture
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o health-controller main.go

# Second stage - Create minimal runtime image using scratch
# Scratch is an empty image, perfect for static binaries
FROM --platform=linux/amd64 scratch

# Copy CA certificates from builder for HTTPS connections
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Set the working directory for the runtime container
WORKDIR /app

# Copy only the compiled binary from the builder stage
# This reduces the final image size by excluding build dependencies
COPY --from=builder /app/health-controller .

# Define the command to run when the container starts
ENTRYPOINT ["./health-controller"]