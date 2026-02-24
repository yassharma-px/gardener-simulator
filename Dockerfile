# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git

# Copy go mod files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o gardener-simulator .

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apk add --no-cache ca-certificates

# Create non-root user
RUN adduser -D -u 1000 simulator

# Copy binary from builder
COPY --from=builder /app/gardener-simulator /app/gardener-simulator

# Create certs directory (certs are dynamically generated at startup)
RUN mkdir -p /app/certs && chown -R simulator:simulator /app

USER simulator

# Expose ports: 8443 (API), 8444 (Management)
EXPOSE 8443 8444

ENTRYPOINT ["/app/gardener-simulator"]
CMD ["--port", "8443", "--cert-dir", "/app/certs"]

