# Stage 1: Build
FROM golang:1.24 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o operation_controller main.go

# Stage 2: Minimal image
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/operation_controller /
USER nonroot:nonroot
ENTRYPOINT ["/operation_controller"]
