package errors

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/types"
)

// Injector handles error injection for testing
type Injector struct {
	mu      sync.RWMutex
	config  types.ErrorInjectionConfig
	enabled bool

	// Simple config for per-operation control
	errorRates map[string]float64 // operation -> error rate
	errorCodes map[string]int     // operation -> error code
	latencyMs  map[string]int     // operation -> latency in ms

	// Per-shoot error injection
	failingShoots map[string]int // namespace/name -> error code

	// Invalid kubeconfig injection
	invalidKubeconfigRate float64
}

// NewInjector creates a new error injector
func NewInjector(cfg types.ErrorInjectionConfig) *Injector {
	return &Injector{
		config:                cfg,
		enabled:               cfg.Enabled,
		errorRates:            make(map[string]float64),
		errorCodes:            make(map[string]int),
		latencyMs:             make(map[string]int),
		failingShoots:         make(map[string]int),
		invalidKubeconfigRate: cfg.InvalidKubeconfigRate,
	}
}

// SetEnabled enables or disables error injection
func (i *Injector) SetEnabled(enabled bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.enabled = enabled
}

// UpdateConfig updates the error injection configuration
func (i *Injector) UpdateConfig(cfg types.ErrorInjectionConfig) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.config = cfg
	i.enabled = cfg.Enabled
	i.invalidKubeconfigRate = cfg.InvalidKubeconfigRate

	// Copy failing shoots
	i.failingShoots = make(map[string]int)
	for k, v := range cfg.FailingShoots {
		i.failingShoots[k] = v
	}
}

// SetFailingShoot marks a specific shoot to always return an error
func (i *Injector) SetFailingShoot(namespace, name string, errorCode int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := namespace + "/" + name
	if errorCode == 0 {
		delete(i.failingShoots, key)
	} else {
		i.failingShoots[key] = errorCode
	}
}

// ClearFailingShoots removes all per-shoot error injections
func (i *Injector) ClearFailingShoots() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failingShoots = make(map[string]int)
}

// ShouldInjectShootError checks if a specific shoot should always fail
func (i *Injector) ShouldInjectShootError(namespace, name string) (bool, int, string) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.enabled {
		return false, 0, ""
	}

	key := namespace + "/" + name
	if code, exists := i.failingShoots[key]; exists {
		return true, code, errorMessage(code, "access shoot "+name)
	}
	return false, 0, ""
}

// ShouldReturnInvalidKubeconfig checks if an invalid kubeconfig should be returned
func (i *Injector) ShouldReturnInvalidKubeconfig() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.enabled || i.invalidKubeconfigRate == 0 {
		return false
	}
	return rand.Float64() < i.invalidKubeconfigRate
}

// SetInvalidKubeconfigRate sets the rate for returning invalid kubeconfigs
func (i *Injector) SetInvalidKubeconfigRate(rate float64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.invalidKubeconfigRate = rate
}

// UpdateSimpleConfig updates the simplified error injection configuration
func (i *Injector) UpdateSimpleConfig(enabled bool, errorRates map[string]float64, errorCodes map[string]int, latencyMs map[string]int) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.enabled = enabled

	// Reset maps
	i.errorRates = make(map[string]float64)
	i.errorCodes = make(map[string]int)
	i.latencyMs = make(map[string]int)

	// Copy values with normalized keys
	for k, v := range errorRates {
		i.errorRates[normalizeOperation(k)] = v
	}
	for k, v := range errorCodes {
		i.errorCodes[normalizeOperation(k)] = v
	}
	for k, v := range latencyMs {
		i.latencyMs[normalizeOperation(k)] = v
	}
}

// normalizeOperation converts operation keys to internal format
func normalizeOperation(op string) string {
	// Convert "adminkubeconfig" -> "AdminKubeconfig", etc.
	op = strings.ToLower(op)
	switch op {
	case "adminkubeconfig", "admin_kubeconfig":
		return "AdminKubeconfig"
	case "listshoots", "list_shoots":
		return "ListShoots"
	case "getshoot", "get_shoot":
		return "GetShoot"
	default:
		return op
	}
}

// MaybeInjectLatency adds artificial latency (global)
func (i *Injector) MaybeInjectLatency() {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.enabled || i.config.MinLatencyMs == 0 {
		return
	}

	latency := i.config.MinLatencyMs
	if i.config.MaxLatencyMs > i.config.MinLatencyMs {
		latency += rand.Intn(i.config.MaxLatencyMs - i.config.MinLatencyMs)
	}
	time.Sleep(time.Duration(latency) * time.Millisecond)
}

// MaybeInjectOperationLatency adds per-operation latency
func (i *Injector) MaybeInjectOperationLatency(operation string) {
	i.mu.RLock()
	latencyMs, exists := i.latencyMs[operation]
	enabled := i.enabled
	i.mu.RUnlock()

	if !enabled || !exists || latencyMs <= 0 {
		return
	}

	time.Sleep(time.Duration(latencyMs) * time.Millisecond)
}

// ShouldInjectError checks if an error should be injected for the given operation
func (i *Injector) ShouldInjectError(operation string) (bool, int, string) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.enabled {
		return false, 0, ""
	}

	// First check simple config (per-operation rates)
	if rate, exists := i.errorRates[operation]; exists && rate > 0 {
		if rand.Float64() < rate {
			code := http.StatusInternalServerError
			if c, exists := i.errorCodes[operation]; exists {
				code = c
			}
			return true, code, errorMessage(code, operation)
		}
		return false, 0, ""
	}

	// Fall back to legacy config
	var rate float64
	switch operation {
	case "ListShoots":
		rate = i.config.ListShootsErrorRate
	case "GetShoot":
		rate = i.config.GetShootErrorRate
	case "AdminKubeconfig":
		rate = i.config.AdminKubeconfigErrorRate
	default:
		return false, 0, ""
	}

	if rate == 0 || rand.Float64() >= rate {
		return false, 0, ""
	}

	// Determine which type of error to inject
	return i.selectErrorType()
}

// errorMessage returns an appropriate error message for the given code
func errorMessage(code int, operation string) string {
	switch code {
	case http.StatusUnauthorized:
		return "Unauthorized: invalid or expired credentials"
	case http.StatusForbidden:
		return "Forbidden: user does not have permission to " + operation
	case http.StatusNotFound:
		return "Not Found: resource does not exist"
	case http.StatusTooManyRequests:
		return "Too Many Requests: API rate limit exceeded, retry after backoff"
	case http.StatusInternalServerError:
		return "Internal Server Error: temporary failure"
	case http.StatusGatewayTimeout:
		return "Gateway Timeout: request took too long"
	case http.StatusServiceUnavailable:
		return "Service Unavailable: server is temporarily unavailable"
	default:
		return http.StatusText(code)
	}
}

func (i *Injector) selectErrorType() (bool, int, string) {
	r := rand.Float64()
	cumulative := 0.0

	// Check auth failure
	cumulative += i.config.AuthFailureRate
	if r < cumulative {
		return true, http.StatusUnauthorized, "Unauthorized: invalid or expired credentials"
	}

	// Check forbidden
	cumulative += i.config.ForbiddenRate
	if r < cumulative {
		return true, http.StatusForbidden, "Forbidden: insufficient permissions"
	}

	// Check not found
	cumulative += i.config.NotFoundRate
	if r < cumulative {
		return true, http.StatusNotFound, "Not Found: resource does not exist"
	}

	// Check server error
	cumulative += i.config.ServerErrorRate
	if r < cumulative {
		return true, http.StatusInternalServerError, "Internal Server Error: temporary failure"
	}

	// Check timeout (we'll simulate this with a long delay)
	cumulative += i.config.TimeoutRate
	if r < cumulative {
		return true, http.StatusGatewayTimeout, "Gateway Timeout: request took too long"
	}

	// Check rate limit (429)
	cumulative += i.config.RateLimitRate
	if r < cumulative {
		return true, http.StatusTooManyRequests, "Too Many Requests: API rate limit exceeded, retry after backoff"
	}

	// Default to server error if we got here
	return true, http.StatusInternalServerError, "Internal Server Error"
}

// WriteError writes an error response in Kubernetes API format
func WriteError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errResp := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Status",
		"metadata":   map[string]interface{}{},
		"status":     "Failure",
		"message":    message,
		"reason":     http.StatusText(statusCode),
		"code":       statusCode,
	}

	json.NewEncoder(w).Encode(errResp)
}
