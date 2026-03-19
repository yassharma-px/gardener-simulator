package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/errors"
	"github.com/yassharma/gardener-simulator/pkg/store"
	"github.com/yassharma/gardener-simulator/pkg/types"
)

// ManagementHandler handles simulator management API requests
type ManagementHandler struct {
	store         *store.ShootStore
	injector      *errors.Injector
	currentConfig *SimpleErrorConfig
	certBundle    CertBundle
	serverURL     string
}

// CertBundle holds certificate data for kubeconfig generation
type CertBundle struct {
	CACert []byte
}

// SimpleErrorConfig is a simplified error config for easier API usage
type SimpleErrorConfig struct {
	Enabled    bool               `json:"enabled"`
	ErrorRates map[string]float64 `json:"error_rates,omitempty"`
	ErrorCodes map[string]int     `json:"error_codes,omitempty"`
	LatencyMs  map[string]int     `json:"latency_ms,omitempty"`
}

// NewManagementHandler creates a new ManagementHandler
func NewManagementHandler(store *store.ShootStore, injector *errors.Injector, caCert []byte, serverURL string) *ManagementHandler {
	return &ManagementHandler{
		store:         store,
		injector:      injector,
		currentConfig: &SimpleErrorConfig{},
		certBundle:    CertBundle{CACert: caCert},
		serverURL:     serverURL,
	}
}

// ServeHTTP implements http.Handler
func (h *ManagementHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("[MGMT] %s %s", r.Method, r.URL.Path)

	path := strings.TrimPrefix(r.URL.Path, "/management")

	switch {
	case path == "/shoots" && r.Method == http.MethodPost:
		h.handleAddShoot(w, r)
	case strings.HasPrefix(path, "/shoots/") && r.Method == http.MethodDelete:
		h.handleDeleteShoot(w, r, path)
	case strings.HasPrefix(path, "/shoots/") && strings.HasSuffix(path, "/status") && r.Method == http.MethodPut:
		h.handleUpdateShootStatus(w, r, path)
	case strings.HasPrefix(path, "/shoots/") && strings.HasSuffix(path, "/fail") && r.Method == http.MethodPost:
		h.handleSetShootFailure(w, r, path)
	case path == "/errors" && r.Method == http.MethodPost:
		h.handleSimpleErrorConfig(w, r)
	case path == "/errors" && r.Method == http.MethodPut:
		h.handleUpdateErrorConfig(w, r)
	case path == "/errors" && r.Method == http.MethodGet:
		h.handleGetErrorConfig(w, r)
	case path == "/errors/enable" && r.Method == http.MethodPost:
		h.handleEnableErrors(w, r)
	case path == "/errors/disable" && r.Method == http.MethodPost:
		h.handleDisableErrors(w, r)
	case path == "/errors/failing-shoots" && r.Method == http.MethodDelete:
		h.handleClearFailingShoots(w, r)
	case path == "/errors/invalid-kubeconfig" && r.Method == http.MethodPost:
		h.handleSetInvalidKubeconfig(w, r)
	case path == "/errors/expired-kubeconfig" && r.Method == http.MethodPost:
		h.handleSetExpiredKubeconfig(w, r)
	case path == "/errors/short-ttl" && r.Method == http.MethodPost:
		h.handleSetShortTTL(w, r)
	case strings.HasPrefix(path, "/shoots/") && strings.HasSuffix(path, "/kubeconfig-behavior") && r.Method == http.MethodPost:
		h.handleSetShootKubeconfigBehavior(w, r, path)
	case path == "/errors/kubeconfig-behaviors" && r.Method == http.MethodDelete:
		h.handleClearKubeconfigBehaviors(w, r)
	case strings.HasPrefix(path, "/serviceaccounts/") && strings.HasSuffix(path, "/fail") && r.Method == http.MethodPost:
		h.handleSetServiceAccountFailure(w, r, path)
	case path == "/errors/failing-serviceaccounts" && r.Method == http.MethodDelete:
		h.handleClearFailingServiceAccounts(w, r)
	case strings.HasPrefix(path, "/serviceaccounts/") && strings.HasSuffix(path, "/kubeconfig-behavior") && r.Method == http.MethodPost:
		h.handleSetServiceAccountKubeconfigBehavior(w, r, path)
	case path == "/errors/serviceaccount-kubeconfig-behaviors" && r.Method == http.MethodDelete:
		h.handleClearServiceAccountKubeconfigBehaviors(w, r)
	case path == "/errors/connection" && r.Method == http.MethodPost:
		h.handleSetConnectionErrors(w, r)
	case path == "/errors/token-expired" && r.Method == http.MethodPost:
		h.handleSetTokenExpiredError(w, r)
	case strings.HasPrefix(path, "/serviceaccounts/") && strings.HasSuffix(path, "/restricted") && r.Method == http.MethodPost:
		h.handleSetServiceAccountRestricted(w, r, path)
	case path == "/errors/restricted-serviceaccounts" && r.Method == http.MethodDelete:
		h.handleClearRestrictedServiceAccounts(w, r)
	case path == "/errors/restricted-serviceaccounts" && r.Method == http.MethodGet:
		h.handleGetRestrictedServiceAccounts(w, r)
	case path == "/status" && r.Method == http.MethodGet:
		h.handleStatus(w, r)
	case path == "/kubeconfig" && r.Method == http.MethodGet:
		h.handleKubeconfig(w, r)
	case strings.HasPrefix(path, "/kubeconfig/serviceaccount/") && r.Method == http.MethodGet:
		h.handleServiceAccountKubeconfig(w, r, path)
	case path == "/healthz" && r.Method == http.MethodGet:
		h.handleHealthz(w, r)
	default:
		errors.WriteError(w, http.StatusNotFound, "management endpoint not found")
	}
}

// AddShootRequest is the request to add a shoot
type AddShootRequest struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	SeedName  string            `json:"seedName"`
	Labels    map[string]string `json:"labels"`
	CloudType string            `json:"cloudType"`
}

func (h *ManagementHandler) handleAddShoot(w http.ResponseWriter, r *http.Request) {
	var req AddShootRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Namespace == "" || req.Name == "" {
		errors.WriteError(w, http.StatusBadRequest, "namespace and name are required")
		return
	}

	cfg := types.ShootConfig{
		Name:      req.Name,
		SeedName:  req.SeedName,
		Labels:    req.Labels,
		CloudType: req.CloudType,
		Status:    types.ShootStatusHealthy,
	}

	h.store.AddShoot(req.Namespace, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "created",
		"message": fmt.Sprintf("shoot %s/%s created", req.Namespace, req.Name),
	})
}

func (h *ManagementHandler) handleDeleteShoot(w http.ResponseWriter, r *http.Request, path string) {
	// Path: /shoots/{namespace}/{name}
	parts := strings.Split(strings.TrimPrefix(path, "/shoots/"), "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /shoots/{namespace}/{name}")
		return
	}

	h.store.DeleteShoot(parts[0], parts[1])

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "deleted",
		"message": fmt.Sprintf("shoot %s/%s deleted", parts[0], parts[1]),
	})
}

func (h *ManagementHandler) handleUpdateErrorConfig(w http.ResponseWriter, r *http.Request) {
	var cfg types.ErrorInjectionConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.UpdateConfig(cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleSimpleErrorConfig handles the simplified POST /errors format
func (h *ManagementHandler) handleSimpleErrorConfig(w http.ResponseWriter, r *http.Request) {
	var cfg SimpleErrorConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Store the current config for GET requests
	h.currentConfig = &cfg

	// Convert simple config to the injector's config format
	h.injector.UpdateSimpleConfig(cfg.Enabled, cfg.ErrorRates, cfg.ErrorCodes, cfg.LatencyMs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"enabled": cfg.Enabled,
		"config":  cfg,
	})
}

// handleGetErrorConfig returns the current error injection configuration
func (h *ManagementHandler) handleGetErrorConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": h.currentConfig.Enabled,
		"config":  h.currentConfig,
	})
}

func (h *ManagementHandler) handleEnableErrors(w http.ResponseWriter, r *http.Request) {
	h.injector.SetEnabled(true)
	h.currentConfig.Enabled = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

func (h *ManagementHandler) handleDisableErrors(w http.ResponseWriter, r *http.Request) {
	h.injector.SetEnabled(false)
	h.currentConfig.Enabled = false
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

func (h *ManagementHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status": "running",
		"simulator": map[string]interface{}{
			"version": "1.0.0",
		},
		"error_injection": map[string]interface{}{
			"enabled": h.currentConfig.Enabled,
			"config":  h.currentConfig,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (h *ManagementHandler) handleKubeconfig(w http.ResponseWriter, r *http.Request) {
	// Build server URL from request if possible, otherwise use configured URL
	serverURL := h.serverURL
	if r.Host != "" {
		// Get the API port (management port - 1 by convention)
		scheme := "https"
		serverURL = fmt.Sprintf("%s://%s", scheme, r.Host)
		// Replace management port with API port
		// Handle both internal ports (8444 -> 8443) and NodePorts (32444 -> 32443)
		serverURL = strings.Replace(serverURL, ":8444", ":8443", 1)
		serverURL = strings.Replace(serverURL, ":32444", ":32443", 1)
	}

	// Check for explicit server URL override
	if override := r.URL.Query().Get("server"); override != "" {
		serverURL = override
	}

	kubeconfig := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []map[string]interface{}{
			{
				"name": "gardener-simulator",
				"cluster": map[string]interface{}{
					"server":                   serverURL,
					"insecure-skip-tls-verify": true,
				},
			},
		},
		"users": []map[string]interface{}{
			{
				"name": "simulator-admin",
				"user": map[string]interface{}{
					"token": "simulator-admin-token",
				},
			},
		},
		"contexts": []map[string]interface{}{
			{
				"name": "gardener-simulator",
				"context": map[string]interface{}{
					"cluster": "gardener-simulator",
					"user":    "simulator-admin",
				},
			},
		},
		"current-context": "gardener-simulator",
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=kubeconfig.yaml")

	// Convert to YAML-like format using JSON (valid YAML)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.Encode(kubeconfig)
}

func (h *ManagementHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleServiceAccountKubeconfig generates a kubeconfig using a token from the specified ServiceAccount.
// Path: /kubeconfig/serviceaccount/{namespace}/{name}
// Query params:
//   - server: override the API server URL
//   - expirationSeconds: token expiration (default: 3600)
func (h *ManagementHandler) handleServiceAccountKubeconfig(w http.ResponseWriter, r *http.Request, path string) {
	// Parse path: /kubeconfig/serviceaccount/{namespace}/{name}
	path = strings.TrimPrefix(path, "/kubeconfig/serviceaccount/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /kubeconfig/serviceaccount/{namespace}/{name}")
		return
	}
	namespace, saName := parts[0], parts[1]

	// Build server URL
	serverURL := h.serverURL
	if r.Host != "" {
		scheme := "https"
		serverURL = fmt.Sprintf("%s://%s", scheme, r.Host)
		serverURL = strings.Replace(serverURL, ":8444", ":8443", 1)
		serverURL = strings.Replace(serverURL, ":32444", ":32443", 1)
	}
	if override := r.URL.Query().Get("server"); override != "" {
		serverURL = override
	}

	// Parse expiration seconds
	expirationSeconds := int64(3600) // Default 1 hour
	if expStr := r.URL.Query().Get("expirationSeconds"); expStr != "" {
		if parsed, err := strconv.ParseInt(expStr, 10, 64); err == nil && parsed > 0 {
			expirationSeconds = parsed
		}
	}

	// Generate the token (similar to handleTokenRequest)
	now := time.Now()
	expirationTime := now.Add(time.Duration(expirationSeconds) * time.Second)
	token := generateMockJWT(namespace, saName, now, expirationTime)

	// Build kubeconfig with the token
	kubeconfig := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []map[string]interface{}{
			{
				"name": "gardener-simulator",
				"cluster": map[string]interface{}{
					"server":                   serverURL,
					"insecure-skip-tls-verify": true,
				},
			},
		},
		"users": []map[string]interface{}{
			{
				"name": fmt.Sprintf("%s-%s", namespace, saName),
				"user": map[string]interface{}{
					"token": token,
				},
			},
		},
		"contexts": []map[string]interface{}{
			{
				"name": "gardener-simulator",
				"context": map[string]interface{}{
					"cluster": "gardener-simulator",
					"user":    fmt.Sprintf("%s-%s", namespace, saName),
				},
			},
		},
		"current-context": "gardener-simulator",
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=kubeconfig-%s-%s.yaml", namespace, saName))

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.Encode(kubeconfig)
}

// UpdateShootStatusRequest is the request to update shoot status
type UpdateShootStatusRequest struct {
	Status string `json:"status"` // Healthy, Unhealthy, Progressing, Hibernated
}

func (h *ManagementHandler) handleUpdateShootStatus(w http.ResponseWriter, r *http.Request, path string) {
	// Path: /shoots/{namespace}/{name}/status
	path = strings.TrimPrefix(path, "/shoots/")
	path = strings.TrimSuffix(path, "/status")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /shoots/{namespace}/{name}/status")
		return
	}
	namespace, name := parts[0], parts[1]

	var req UpdateShootStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	status := types.ShootStatus(req.Status)
	if err := h.store.UpdateShootStatus(namespace, name, status); err != nil {
		errors.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "updated",
		"message": fmt.Sprintf("shoot %s/%s status set to %s", namespace, name, req.Status),
	})
}

// SetShootFailureRequest configures a shoot to always fail
type SetShootFailureRequest struct {
	ErrorCode int `json:"errorCode"` // 0 to clear, or HTTP status code (401, 403, 404, 500, etc.)
}

func (h *ManagementHandler) handleSetShootFailure(w http.ResponseWriter, r *http.Request, path string) {
	// Path: /shoots/{namespace}/{name}/fail
	path = strings.TrimPrefix(path, "/shoots/")
	path = strings.TrimSuffix(path, "/fail")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /shoots/{namespace}/{name}/fail")
		return
	}
	namespace, name := parts[0], parts[1]

	var req SetShootFailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.SetFailingShoot(namespace, name, req.ErrorCode)

	msg := fmt.Sprintf("shoot %s/%s will now return %d errors", namespace, name, req.ErrorCode)
	if req.ErrorCode == 0 {
		msg = fmt.Sprintf("shoot %s/%s failure injection cleared", namespace, name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "configured",
		"message": msg,
	})
}

func (h *ManagementHandler) handleClearFailingShoots(w http.ResponseWriter, r *http.Request) {
	h.injector.ClearFailingShoots()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-shoot failure injections cleared",
	})
}

// SetInvalidKubeconfigRequest configures invalid kubeconfig rate
type SetInvalidKubeconfigRequest struct {
	Rate float64 `json:"rate"` // 0.0 to 1.0
}

func (h *ManagementHandler) handleSetInvalidKubeconfig(w http.ResponseWriter, r *http.Request) {
	var req SetInvalidKubeconfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Rate < 0 || req.Rate > 1 {
		errors.WriteError(w, http.StatusBadRequest, "rate must be between 0.0 and 1.0")
		return
	}

	h.injector.SetInvalidKubeconfigRate(req.Rate)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": fmt.Sprintf("invalid kubeconfig rate set to %.2f", req.Rate),
		"rate":    req.Rate,
	})
}

// SetExpiredKubeconfigRequest configures expired kubeconfig rate
type SetExpiredKubeconfigRequest struct {
	Rate float64 `json:"rate"` // 0.0 to 1.0
}

func (h *ManagementHandler) handleSetExpiredKubeconfig(w http.ResponseWriter, r *http.Request) {
	var req SetExpiredKubeconfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Rate < 0 || req.Rate > 1 {
		errors.WriteError(w, http.StatusBadRequest, "rate must be between 0.0 and 1.0")
		return
	}

	h.injector.SetExpiredKubeconfigRate(req.Rate)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": fmt.Sprintf("expired kubeconfig rate set to %.2f", req.Rate),
		"rate":    req.Rate,
	})
}

// SetShortTTLRequest configures short TTL override for testing rapid refresh
type SetShortTTLRequest struct {
	Seconds int64 `json:"seconds"` // 0 to disable, or number of seconds
}

func (h *ManagementHandler) handleSetShortTTL(w http.ResponseWriter, r *http.Request) {
	var req SetShortTTLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Seconds < 0 {
		errors.WriteError(w, http.StatusBadRequest, "seconds must be >= 0")
		return
	}

	h.injector.SetShortTTLSeconds(req.Seconds)

	msg := fmt.Sprintf("short TTL override set to %d seconds", req.Seconds)
	if req.Seconds == 0 {
		msg = "short TTL override disabled"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": msg,
		"seconds": req.Seconds,
	})
}

// SetShootKubeconfigBehaviorRequest configures per-shoot kubeconfig behavior
type SetShootKubeconfigBehaviorRequest struct {
	Behavior string `json:"behavior"` // "expired", "invalid", or "" to clear
}

func (h *ManagementHandler) handleSetShootKubeconfigBehavior(w http.ResponseWriter, r *http.Request, path string) {
	// Path: /shoots/{namespace}/{name}/kubeconfig-behavior
	path = strings.TrimPrefix(path, "/shoots/")
	path = strings.TrimSuffix(path, "/kubeconfig-behavior")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /shoots/{namespace}/{name}/kubeconfig-behavior")
		return
	}
	namespace, name := parts[0], parts[1]

	var req SetShootKubeconfigBehaviorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	validBehaviors := map[string]bool{"": true, "expired": true, "invalid": true}
	if !validBehaviors[req.Behavior] {
		errors.WriteError(w, http.StatusBadRequest, "behavior must be 'expired', 'invalid', or empty string to clear")
		return
	}

	h.injector.SetShootKubeconfigBehavior(namespace, name, req.Behavior)

	msg := fmt.Sprintf("shoot %s/%s kubeconfig behavior set to '%s'", namespace, name, req.Behavior)
	if req.Behavior == "" {
		msg = fmt.Sprintf("shoot %s/%s kubeconfig behavior cleared", namespace, name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "configured",
		"message":  msg,
		"behavior": req.Behavior,
	})
}

func (h *ManagementHandler) handleClearKubeconfigBehaviors(w http.ResponseWriter, r *http.Request) {
	h.injector.ClearShootKubeconfigBehaviors()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-shoot kubeconfig behaviors cleared",
	})
}

// SetServiceAccountFailureRequest configures a service account to always fail TokenRequest
type SetServiceAccountFailureRequest struct {
	ErrorCode int `json:"errorCode"` // 0 to clear, or HTTP status code (401, 403, 404, 429, 500, etc.)
}

func (h *ManagementHandler) handleSetServiceAccountFailure(w http.ResponseWriter, r *http.Request, path string) {
	// Parse path: /serviceaccounts/{namespace}/{name}/fail
	path = strings.TrimPrefix(path, "/serviceaccounts/")
	path = strings.TrimSuffix(path, "/fail")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /serviceaccounts/{namespace}/{name}/fail")
		return
	}
	namespace, name := parts[0], parts[1]

	var req SetServiceAccountFailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.SetFailingServiceAccount(namespace, name, req.ErrorCode)

	msg := fmt.Sprintf("serviceaccount %s/%s will now return %d errors for TokenRequest", namespace, name, req.ErrorCode)
	if req.ErrorCode == 0 {
		msg = fmt.Sprintf("serviceaccount %s/%s TokenRequest failure injection cleared", namespace, name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "configured",
		"message": msg,
	})
}

func (h *ManagementHandler) handleClearFailingServiceAccounts(w http.ResponseWriter, r *http.Request) {
	h.injector.ClearFailingServiceAccounts()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-ServiceAccount failure injections cleared",
	})
}

// SetServiceAccountKubeconfigBehaviorRequest configures kubeconfig behavior for a service account
type SetServiceAccountKubeconfigBehaviorRequest struct {
	// Behavior: "expired" (return expired token), "invalid" (return malformed token),
	// "token_expired_error" (return 401 with "token expired" message), "" (clear)
	Behavior string `json:"behavior"`
}

func (h *ManagementHandler) handleSetServiceAccountKubeconfigBehavior(w http.ResponseWriter, r *http.Request, path string) {
	// Parse path: /serviceaccounts/{namespace}/{name}/kubeconfig-behavior
	trimmed := strings.TrimPrefix(path, "/serviceaccounts/")
	trimmed = strings.TrimSuffix(trimmed, "/kubeconfig-behavior")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path format, expected /serviceaccounts/{namespace}/{name}/kubeconfig-behavior")
		return
	}

	namespace := parts[0]
	name := parts[1]

	var req SetServiceAccountKubeconfigBehaviorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate behavior
	validBehaviors := map[string]bool{"": true, "expired": true, "invalid": true, "token_expired_error": true}
	if !validBehaviors[req.Behavior] {
		errors.WriteError(w, http.StatusBadRequest, "invalid behavior, must be one of: expired, invalid, token_expired_error, or empty to clear")
		return
	}

	h.injector.SetServiceAccountKubeconfigBehavior(namespace, name, req.Behavior)

	msg := fmt.Sprintf("serviceaccount %s/%s kubeconfig behavior set to '%s'", namespace, name, req.Behavior)
	if req.Behavior == "" {
		msg = fmt.Sprintf("serviceaccount %s/%s kubeconfig behavior cleared", namespace, name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "configured",
		"message": msg,
	})
}

func (h *ManagementHandler) handleClearServiceAccountKubeconfigBehaviors(w http.ResponseWriter, r *http.Request) {
	h.injector.ClearServiceAccountKubeconfigBehaviors()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-ServiceAccount kubeconfig behaviors cleared",
	})
}

// SetConnectionErrorsRequest configures connection-level error injection
type SetConnectionErrorsRequest struct {
	ConnectionRefusedRate float64 `json:"connectionRefusedRate"` // Rate for "connection refused" errors
	ConnectionResetRate   float64 `json:"connectionResetRate"`   // Rate for "connection reset" errors
	IOTimeoutRate         float64 `json:"ioTimeoutRate"`         // Rate for "i/o timeout" errors
}

func (h *ManagementHandler) handleSetConnectionErrors(w http.ResponseWriter, r *http.Request) {
	var req SetConnectionErrorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.SetConnectionErrorRates(req.ConnectionRefusedRate, req.ConnectionResetRate, req.IOTimeoutRate)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": "connection error rates configured",
		"config": map[string]float64{
			"connectionRefusedRate": req.ConnectionRefusedRate,
			"connectionResetRate":   req.ConnectionResetRate,
			"ioTimeoutRate":         req.IOTimeoutRate,
		},
	})
}

// SetTokenExpiredErrorRequest configures token expired error injection
type SetTokenExpiredErrorRequest struct {
	Rate float64 `json:"rate"` // Rate for "token expired" errors (0.0 to 1.0)
}

func (h *ManagementHandler) handleSetTokenExpiredError(w http.ResponseWriter, r *http.Request) {
	var req SetTokenExpiredErrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.SetTokenExpiredErrorRate(req.Rate)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": fmt.Sprintf("token expired error rate set to %.2f", req.Rate),
		"rate":    req.Rate,
	})
}

// SetServiceAccountRestrictedRequest configures restricted access for a service account
type SetServiceAccountRestrictedRequest struct {
	Restricted bool `json:"restricted"` // true to restrict, false to unrestrict
}

func (h *ManagementHandler) handleSetServiceAccountRestricted(w http.ResponseWriter, r *http.Request, path string) {
	// Path: /serviceaccounts/{namespace}/{name}/restricted
	path = strings.TrimPrefix(path, "/serviceaccounts/")
	path = strings.TrimSuffix(path, "/restricted")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		errors.WriteError(w, http.StatusBadRequest, "invalid path, expected /serviceaccounts/{namespace}/{name}/restricted")
		return
	}
	namespace, name := parts[0], parts[1]

	var req SetServiceAccountRestrictedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.injector.SetRestrictedServiceAccount(namespace, name, req.Restricted)

	msg := fmt.Sprintf("serviceaccount %s/%s is now restricted", namespace, name)
	if !req.Restricted {
		msg = fmt.Sprintf("serviceaccount %s/%s restriction removed", namespace, name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "configured",
		"message":    msg,
		"namespace":  namespace,
		"name":       name,
		"restricted": req.Restricted,
	})
}

func (h *ManagementHandler) handleClearRestrictedServiceAccounts(w http.ResponseWriter, r *http.Request) {
	h.injector.ClearRestrictedServiceAccounts()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all restricted service account markers cleared",
	})
}

func (h *ManagementHandler) handleGetRestrictedServiceAccounts(w http.ResponseWriter, r *http.Request) {
	restricted := h.injector.GetRestrictedServiceAccounts()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"restrictedServiceAccounts": restricted,
	})
}
