# Multi-stage build for optimal image size and security
FROM golang:1.24.3 AS builder

# Set build arguments for better reproducibility
ARG CGO_ENABLED=0
ARG GOOS=linux
ARG GOARCH=amd64

# Install git and ca-certificates for dependency fetching
RUN apt-get update && apt-get install -y git ca-certificates && rm -rf /var/lib/apt/lists/*

# Create non-root user for build process
RUN useradd -m -s /bin/bash appuser

# Set working directory
WORKDIR /app

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application with optimizations
RUN CGO_ENABLED=${CGO_ENABLED} GOOS=${GOOS} GOARCH=${GOARCH} \
    go build -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o kops ./main.go

# Final stage - minimal runtime image
FROM scratch

# Import ca-certificates from builder for HTTPS calls to Azure
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Import the user and group files from the builder
COPY --from=builder /etc/passwd /etc/passwd

# Copy the binary from builder stage
COPY --from=builder /app/kops /kops

# Use non-root user
USER appuser

# Health check endpoint (if you add one later)
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
#   CMD ["/kops-metrics", "--health-check"] || exit 1

# Labels for better container management
LABEL maintainer="mutumhe09@gmail.com"
LABEL description="Kubernetes metrics collector for AKS clusters"
LABEL version="1.0.0"
LABEL org.opencontainers.image.source="https://github.com/danekeahi/kops"

# Set environment variables with defaults
ENV AZURE_SUBSCRIPTION_ID=""
ENV AKS_RESOURCE_GROUP=""
ENV AKS_CLUSTER_NAME=""

# Expose port if you add HTTP endpoints later
# EXPOSE 8080

# Run the binary
ENTRYPOINT ["/kops"]