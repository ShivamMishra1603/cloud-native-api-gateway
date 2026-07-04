# Stage 1: Build the Go binary
FROM golang:1.25.1-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application with optimizations (disable CGO, strip debug symbols)
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway ./cmd/gateway

# Stage 2: Create final lightweight container
FROM alpine:3.20

# Install ca-certificates for upstream HTTPS calls and curl for health checks
RUN apk add --no-cache ca-certificates curl

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/gateway .

# Copy default configurations
COPY configs/ /app/configs/

# Expose default API gateway port
EXPOSE 8080

# Command to run
ENTRYPOINT ["./gateway"]
