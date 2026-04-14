# Build stage
FROM golang:1.26-alpine AS builder

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

# Download envtest binaries (etcd and kube-apiserver) for envtest mode
# These are needed by controller-runtime's envtest package
ARG ENVTEST_K8S_VERSION=1.31.0
RUN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest && \
    setup-envtest use ${ENVTEST_K8S_VERSION} --bin-dir /envtest-bins -p path

# Runtime stage - use debian for envtest mode compatibility
FROM debian:bookworm-slim

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -m -u 1000 simulator

# Copy binary from builder
COPY --from=builder /app/gardener-simulator /app/gardener-simulator

# Copy CRDs for envtest mode
COPY --from=builder /app/crds /app/crds

# Copy envtest binaries for envtest mode
COPY --from=builder /envtest-bins /app/envtest-bins

# Set envtest binary path (must match version in builder stage)
ENV KUBEBUILDER_ASSETS=/app/envtest-bins/k8s/1.31.0-linux-amd64

# Create certs directory (certs are dynamically generated at startup)
RUN mkdir -p /app/certs && chown -R simulator:simulator /app

USER simulator

# Expose ports: 8443 (API), 8444 (Management)
EXPOSE 8443 8444

ENTRYPOINT ["/app/gardener-simulator"]
CMD ["--port", "8443", "--cert-dir", "/app/certs"]

