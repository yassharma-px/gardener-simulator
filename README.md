# Gardener Cluster Simulator

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.25+-326CE5?style=flat&logo=kubernetes)](https://kubernetes.io/)
[![Helm](https://img.shields.io/badge/Helm-3.0+-0F1689?style=flat&logo=helm)](https://helm.sh/)

A lightweight simulator for the [SAP Gardener](https://gardener.cloud/) API. Test Gardener client applications, validate error handling, and simulate large-scale cluster environments without requiring a real Gardener installation.

## Overview

The Gardener Simulator implements the `core.gardener.cloud/v1beta1` API, enabling you to:

- **Develop offline** - Build and test Gardener integrations without cluster access
- **Scale testing** - Simulate hundreds of shoots across multiple projects
- **Chaos engineering** - Inject errors, latency, and failures for resilience testing
- **CI/CD pipelines** - Run integration tests in isolated environments

## Quick Start

### Helm (Recommended)

```bash
helm install gs https://github.com/yassharma-px/gardener-simulator/raw/main/helm/gardener-simulator-0.1.0.tgz
```

By default, the simulator creates **1 project** with **10 shoots**. Projects are named `project-0`, `project-1`, etc. Shoots are named `shoot-0`, `shoot-1`, etc.

Retrieve the kubeconfig and start using kubectl:

```bash
# Get kubeconfig (port 32444 is the management API)
curl http://<node-ip>:32444/management/kubeconfig > kubeconfig.yaml

# Use kubectl
kubectl --kubeconfig=kubeconfig.yaml get shoots --all-namespaces
```

### Custom Configuration

```bash
# Large-scale simulation: 10 projects × 50 shoots = 500 clusters
helm install gs https://github.com/yassharma-px/gardener-simulator/raw/main/helm/gardener-simulator-0.1.0.tgz \
  --namespace gardener-sim \
  --create-namespace \
  --set simulator.projects=10 \
  --set simulator.shoots=50
```

## Installation

### Prerequisites

- Kubernetes 1.25+ (for Helm/kubectl deployment)
- Go 1.21+ (for local development)
- Helm 3.0+ (for chart installation)

### Option 1: Helm Chart

```bash
# Basic installation
helm install gs https://github.com/yassharma-px/gardener-simulator/raw/main/helm/gardener-simulator-0.1.0.tgz

# Uninstall
helm uninstall gs
```

### Option 2: Kubernetes Manifests

```bash
kubectl apply -f deploy/kubernetes/deployment.yaml
```

### Option 3: Local Development

```bash
# Build
make build

# Run with defaults (1 project, 10 shoots)
make run

# Or run directly with custom flags
./bin/gardener-simulator --projects 5 --shoots 100
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Gardener Simulator                       │
├─────────────────────────────┬───────────────────────────────┤
│   Gardener API (HTTPS)      │   Management API (HTTP)       │
│   Port: 8443                │   Port: 8444                  │
├─────────────────────────────┼───────────────────────────────┤
│ • List/Get Shoots           │ • Dynamic shoot management    │
│ • Admin Kubeconfig          │ • Error injection control     │
│ • Namespace isolation       │ • Runtime kubeconfig          │
│                             │ • Health checks               │
├─────────────────────────────┴───────────────────────────────┤
│                    In-Memory Store                          │
│              (Projects, Shoots, Certificates)               │
└─────────────────────────────────────────────────────────────┘
```

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8443` | HTTPS port for Gardener API |
| `--management-port` | `8444` | HTTP port for Management API |
| `--projects` | `1` | Number of simulated projects |
| `--shoots` | `10` | Number of shoots per project |
| `--config` | (none) | Path to YAML configuration file |

### Helm Values

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `pure-artifactory.../gardener-sim` | Container image |
| `image.tag` | `latest` | Image tag |
| `simulator.projects` | `1` | Number of projects |
| `simulator.shoots` | `10` | Shoots per project |
| `service.type` | `NodePort` | Service type (NodePort, ClusterIP, LoadBalancer) |
| `service.apiNodePort` | `32443` | Gardener API NodePort |
| `service.managementNodePort` | `32444` | Management API NodePort |

### Configuration File

For complex scenarios, use a YAML configuration file. See [`examples/config.yaml`](examples/config.yaml) for the full schema.

## API Reference

### Gardener API (HTTPS)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/apis/core.gardener.cloud/v1beta1/namespaces/{ns}/shoots` | GET | List shoots in namespace |
| `/apis/core.gardener.cloud/v1beta1/namespaces/{ns}/shoots/{name}` | GET | Get shoot details |
| `/apis/core.gardener.cloud/v1beta1/namespaces/{ns}/shoots/{name}/adminkubeconfig` | POST | Generate admin kubeconfig |
| `/apis/core.gardener.cloud/v1beta1/projects` | GET | List all projects |
| `/apis/core.gardener.cloud/v1beta1/projects/{name}` | GET | Get project details |

### Management API (HTTP)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/management/kubeconfig` | GET | Download simulator kubeconfig |
| `/management/shoots` | POST | Create a new shoot |
| `/management/shoots/{ns}/{name}` | DELETE | Delete a shoot |
| `/management/shoots/{ns}/{name}/status` | PUT | Update shoot status (Healthy/Unhealthy/Hibernated) |
| `/management/shoots/{ns}/{name}/fail` | POST | Configure shoot to always fail with specific error |
| `/management/errors` | PUT | Configure error injection |
| `/management/errors` | GET | Get current error configuration |
| `/management/errors/enable` | POST | Enable error injection |
| `/management/errors/disable` | POST | Disable error injection |
| `/management/errors/failing-shoots` | DELETE | Clear all per-shoot failure injections |
| `/management/errors/invalid-kubeconfig` | POST | Set invalid kubeconfig return rate |
| `/management/status` | GET | Get simulator status |
| `/healthz` | GET | Health check (for K8s probes) |

## Usage Examples

### Basic kubectl Operations

```bash
# Get kubeconfig (management API is on port 32444)
curl -s http://<node-ip>:32444/management/kubeconfig > kubeconfig.yaml

# List all shoots
kubectl --kubeconfig=kubeconfig.yaml get shoots -A

# Get shoot details
kubectl --kubeconfig=kubeconfig.yaml get shoot alpha -n garden-project-0 -o yaml

# Generate admin kubeconfig for a shoot
kubectl --kubeconfig=kubeconfig.yaml create -f - <<EOF
apiVersion: core.gardener.cloud/v1beta1
kind: AdminKubeconfigRequest
metadata:
  name: alpha
  namespace: garden-project-0
EOF
```

### Dynamic Shoot Management

```bash
# Add a shoot (management API is on port 32444)
curl -X POST http://<node-ip>:32444/management/shoots \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "garden-production",
    "name": "new-cluster",
    "cloudType": "aws",
    "labels": {"env": "prod", "team": "platform"}
  }'

# Remove a shoot
curl -X DELETE http://localhost:8444/management/shoots/garden-production/new-cluster
```

## Error Injection & Chaos Testing

The simulator includes a powerful error injection system for testing application resilience. **All error rates default to 0** (disabled) and must be explicitly configured.

### How Error Injection Works

Error injection uses a **two-stage process**:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         API Request Received                            │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  STAGE 1: Should an error occur?                                        │
│                                                                         │
│  Check operation error rate (listShootsErrorRate, getShootErrorRate,    │
│  adminKubeconfigErrorRate). Generate random number 0.0-1.0.             │
│                                                                         │
│  If random < errorRate → proceed to Stage 2                             │
│  If random >= errorRate → return SUCCESS (normal response)              │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                          (error triggered)
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  STAGE 2: Which error type to return?                                   │
│                                                                         │
│  Use weighted random selection based on error type rates:               │
│                                                                         │
│  ├─ authFailureRate    → 401 Unauthorized                               │
│  ├─ forbiddenRate      → 403 Forbidden                                  │
│  ├─ notFoundRate       → 404 Not Found                                  │
│  ├─ serverErrorRate    → 500 Internal Server Error                      │
│  └─ timeoutRate        → 504 Gateway Timeout                            │
│                                                                         │
│  Error type is selected proportionally to their configured rates.       │
└─────────────────────────────────────────────────────────────────────────┘
```

### Configuration Parameters

#### Stage 1: Operation Error Rates

These control **IF** an error occurs for each API operation. Values range from `0.0` (never fails) to `1.0` (always fails):

| Parameter | Description | Default |
|-----------|-------------|---------|
| `listShootsErrorRate` | Probability of error when listing shoots | `0.0` |
| `getShootErrorRate` | Probability of error when getting a single shoot | `0.0` |
| `adminKubeconfigErrorRate` | Probability of error when generating admin kubeconfig | `0.0` |

#### Stage 2: Error Type Distribution

These control **WHICH** error is returned when a failure occurs. The rates act as weights for random selection:

| Parameter | HTTP Status | Description | Default |
|-----------|-------------|-------------|---------|
| `authFailureRate` | 401 | Unauthorized - Invalid/expired credentials | `0.0` |
| `forbiddenRate` | 403 | Forbidden - Insufficient permissions | `0.0` |
| `notFoundRate` | 404 | Not Found - Resource doesn't exist | `0.0` |
| `serverErrorRate` | 500 | Internal Server Error - Temporary failure | `0.0` |
| `timeoutRate` | 504 | Gateway Timeout - Request took too long | `0.0` |
| `rateLimitRate` | 429 | Too Many Requests - API rate limit exceeded | `0.0` |

#### Special Response Modes

| Parameter | Description | Default |
|-----------|-------------|---------|
| `invalidKubeconfigRate` | Probability of returning malformed kubeconfig (for validation testing) | `0.0` |

#### Latency Injection

Add artificial delay to **all** requests (in milliseconds), independent of error injection:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `minLatencyMs` | Minimum latency added to each request | `0` |
| `maxLatencyMs` | Maximum latency (actual delay is random between min/max) | `0` |

### Examples

#### Example 1: Random Mixed Errors

This configuration causes 30% of `ListShoots` calls to fail with randomly selected error types:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "listShootsErrorRate": 0.3,
    "authFailureRate": 0.2,
    "forbiddenRate": 0.1,
    "serverErrorRate": 0.5,
    "timeoutRate": 0.2
  }'
```

**What happens:**
- 70% of requests → Success (normal response)
- 30% of requests → Error, distributed as:
  - ~20% of errors → 401 Unauthorized
  - ~10% of errors → 403 Forbidden
  - ~50% of errors → 500 Internal Server Error
  - ~20% of errors → 504 Gateway Timeout

**Out of 100 requests, you'd see approximately:**
- 70 successful responses
- 6 × 401 errors
- 3 × 403 errors
- 15 × 500 errors
- 6 × 504 errors

#### Example 2: Only 401 Unauthorized Errors

To test authentication handling, configure errors to **always** return 401:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "listShootsErrorRate": 0.2,
    "getShootErrorRate": 0.2,
    "adminKubeconfigErrorRate": 0.5,
    "authFailureRate": 1.0,
    "forbiddenRate": 0.0,
    "serverErrorRate": 0.0,
    "timeoutRate": 0.0
  }'
```

**What happens:**
- 20% of `ListShoots` calls fail with 401
- 20% of `GetShoot` calls fail with 401
- 50% of `AdminKubeconfig` calls fail with 401
- All errors are 401 (since `authFailureRate: 1.0` and others are `0.0`)

#### Example 3: Only 500 Internal Server Errors

To test retry logic for server errors:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "listShootsErrorRate": 0.4,
    "getShootErrorRate": 0.4,
    "serverErrorRate": 1.0
  }'
```

**What happens:**
- 40% of list/get operations fail
- All failures return 500 Internal Server Error

#### Example 4: Latency Without Errors

Add 200-800ms delay to all requests without causing failures:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "minLatencyMs": 200,
    "maxLatencyMs": 800
  }'
```

**What happens:**
- All requests succeed
- Each request has a random 200-800ms delay added

#### Example 5: Realistic Production Simulation

Simulate a degraded but mostly working API:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "listShootsErrorRate": 0.02,
    "getShootErrorRate": 0.01,
    "adminKubeconfigErrorRate": 0.05,
    "serverErrorRate": 0.7,
    "timeoutRate": 0.3,
    "minLatencyMs": 50,
    "maxLatencyMs": 300
  }'
```

**What happens:**
- 98% of list calls succeed (2% fail)
- 99% of get calls succeed (1% fail)
- 95% of kubeconfig calls succeed (5% fail)
- When errors occur: 70% are 500, 30% are 504
- All requests have 50-300ms additional latency

#### Example 6: 429 Rate Limiting (Test Backoff Logic)

Simulate API rate limiting to test your application's exponential backoff:

```bash
curl -X PUT http://localhost:8444/management/errors \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "listShootsErrorRate": 0.5,
    "rateLimitRate": 1.0
  }'
```

**Result:** 50% of list operations return `429 Too Many Requests`.

#### Example 7: Per-Shoot Failure Injection

Make a specific shoot always fail (useful for testing partial failure handling):

```bash
# Make shoot "my-cluster" in namespace "garden-production" always return 404
curl -X POST http://localhost:8444/management/shoots/garden-production/my-cluster/fail \
  -H "Content-Type: application/json" \
  -d '{"errorCode": 404}'

# Clear the failure injection
curl -X POST http://localhost:8444/management/shoots/garden-production/my-cluster/fail \
  -H "Content-Type: application/json" \
  -d '{"errorCode": 0}'

# Clear ALL per-shoot failure injections
curl -X DELETE http://localhost:8444/management/errors/failing-shoots
```

#### Example 8: Invalid Kubeconfig Response (Test Validation)

Test how your application handles malformed kubeconfig responses:

```bash
# 30% of kubeconfig requests return invalid/malformed data
curl -X POST http://localhost:8444/management/errors/invalid-kubeconfig \
  -H "Content-Type: application/json" \
  -d '{"rate": 0.3}'
```

#### Example 9: Dynamic Shoot Status Updates

Change shoot status to test status monitoring logic:

```bash
# Set shoot to unhealthy
curl -X PUT http://localhost:8444/management/shoots/garden-production/my-cluster/status \
  -H "Content-Type: application/json" \
  -d '{"status": "Unhealthy"}'

# Set shoot to hibernated
curl -X PUT http://localhost:8444/management/shoots/garden-production/my-cluster/status \
  -H "Content-Type: application/json" \
  -d '{"status": "Hibernated"}'

# Restore to healthy
curl -X PUT http://localhost:8444/management/shoots/garden-production/my-cluster/status \
  -H "Content-Type: application/json" \
  -d '{"status": "Healthy"}'
```

### Quick Commands

```bash
# Enable error injection (uses current config)
curl -X POST http://localhost:8444/management/errors/enable

# Disable error injection (keeps config for later)
curl -X POST http://localhost:8444/management/errors/disable

# Check current configuration
curl http://localhost:8444/management/errors

# Get simulator status including error config
curl http://localhost:8444/management/status
```

## Development

```bash
# Build binary
make build

# Run tests
make test

# Build Docker image
make docker-build

# Build and push image
make docker

# Package Helm chart
make helm-package
```

