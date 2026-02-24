package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/errors"
	"github.com/yassharma/gardener-simulator/pkg/kubeconfig"
	"github.com/yassharma/gardener-simulator/pkg/store"
	"github.com/yassharma/gardener-simulator/pkg/types"
)

// Handler handles Gardener API requests
type Handler struct {
	store         *store.ShootStore
	injector      *errors.Injector
	kubegen       *kubeconfig.Generator
	serverURL     string
	kubeconfigTTL time.Duration
}

// NewHandler creates a new Handler
func NewHandler(store *store.ShootStore, injector *errors.Injector, kubegen *kubeconfig.Generator, serverURL string, ttl time.Duration) *Handler {
	return &Handler{
		store:         store,
		injector:      injector,
		kubegen:       kubegen,
		serverURL:     serverURL,
		kubeconfigTTL: ttl,
	}
}

// ServeHTTP implements http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("[%s] %s %s", time.Now().Format(time.RFC3339), r.Method, r.URL.Path)

	h.injector.MaybeInjectLatency()

	path := r.URL.Path

	// Route requests
	switch {
	case strings.HasPrefix(path, "/apis/core.gardener.cloud/v1beta1/namespaces/"):
		h.handleNamespacedResource(w, r)
	case path == "/apis/core.gardener.cloud/v1beta1/shoots" || path == "/apis/core.gardener.cloud/v1beta1/shoots/":
		// Cluster-scoped list of all shoots (kubectl get shoots -A)
		h.handleListAllShoots(w, r)
	case strings.HasPrefix(path, "/apis/core.gardener.cloud/v1beta1/projects"):
		h.handleProjects(w, r)
	case path == "/apis/core.gardener.cloud/v1beta1" || path == "/apis/core.gardener.cloud/v1beta1/":
		h.handleGardenerAPIResources(w, r)
	case path == "/api" || path == "/api/":
		h.handleAPIDiscovery(w, r)
	case path == "/api/v1" || path == "/api/v1/":
		h.handleCoreAPIResources(w, r)
	case path == "/api/v1/namespaces" || path == "/api/v1/namespaces/":
		h.handleListNamespaces(w, r)
	case path == "/apis" || path == "/apis/":
		h.handleAPIsDiscovery(w, r)
	case path == "/healthz":
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	case strings.HasPrefix(path, "/openapi"):
		// Return 404 for OpenAPI - kubectl will skip validation
		errors.WriteError(w, http.StatusNotFound, "openapi not available")
	default:
		errors.WriteError(w, http.StatusNotFound, fmt.Sprintf("path not found: %s", path))
	}
}

func (h *Handler) handleNamespacedResource(w http.ResponseWriter, r *http.Request) {
	// Parse path: /apis/core.gardener.cloud/v1beta1/namespaces/{namespace}/shoots[/{name}[/adminkubeconfig]]
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/apis/core.gardener.cloud/v1beta1/namespaces/"), "/")

	if len(parts) < 2 {
		errors.WriteError(w, http.StatusNotFound, "invalid path")
		return
	}

	namespace := parts[0]
	resource := parts[1]

	if resource != "shoots" {
		errors.WriteError(w, http.StatusNotFound, fmt.Sprintf("unknown resource: %s", resource))
		return
	}

	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		h.handleListShoots(w, r, namespace)
	case len(parts) == 3 && r.Method == http.MethodGet:
		h.handleGetShoot(w, r, namespace, parts[2])
	case len(parts) == 4 && parts[3] == "adminkubeconfig" && r.Method == http.MethodPost:
		h.handleAdminKubeconfig(w, r, namespace, parts[2])
	default:
		errors.WriteError(w, http.StatusNotFound, "invalid path or method")
	}
}

func (h *Handler) handleListShoots(w http.ResponseWriter, r *http.Request, namespace string) {
	if inject, code, msg := h.injector.ShouldInjectError("ListShoots"); inject {
		errors.WriteError(w, code, msg)
		return
	}

	labelSelector := r.URL.Query().Get("labelSelector")
	list, err := h.store.ListShoots(namespace, labelSelector)
	if err != nil {
		errors.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// handleListAllShoots handles GET /apis/core.gardener.cloud/v1beta1/shoots (cluster-scoped)
func (h *Handler) handleListAllShoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errors.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if inject, code, msg := h.injector.ShouldInjectError("ListShoots"); inject {
		errors.WriteError(w, code, msg)
		return
	}

	labelSelector := r.URL.Query().Get("labelSelector")
	list, err := h.store.ListShoots("", labelSelector) // Empty namespace = all namespaces
	if err != nil {
		errors.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *Handler) handleGetShoot(w http.ResponseWriter, r *http.Request, namespace, name string) {
	// Check per-shoot error injection first
	if inject, code, msg := h.injector.ShouldInjectShootError(namespace, name); inject {
		errors.WriteError(w, code, msg)
		return
	}

	if inject, code, msg := h.injector.ShouldInjectError("GetShoot"); inject {
		errors.WriteError(w, code, msg)
		return
	}

	shoot, err := h.store.GetShoot(namespace, name)
	if err != nil {
		errors.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shoot)
}

func (h *Handler) handleAdminKubeconfig(w http.ResponseWriter, r *http.Request, namespace, name string) {
	// Check per-shoot error injection
	if inject, code, msg := h.injector.ShouldInjectShootError(namespace, name); inject {
		errors.WriteError(w, code, msg)
		return
	}

	if inject, code, msg := h.injector.ShouldInjectError("AdminKubeconfig"); inject {
		errors.WriteError(w, code, msg)
		return
	}

	// Verify shoot exists
	shoot, err := h.store.GetShoot(namespace, name)
	if err != nil {
		errors.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	// Parse request for TTL
	var req types.AdminKubeconfigRequest
	ttl := h.kubeconfigTTL
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Spec.ExpirationSeconds > 0 {
			ttl = time.Duration(req.Spec.ExpirationSeconds) * time.Second
		}
	}

	// Check if we should return an invalid kubeconfig
	if h.injector.ShouldReturnInvalidKubeconfig() {
		// Return malformed kubeconfig for testing validation
		resp := types.AdminKubeconfigResponse{
			APIVersion: "authentication.gardener.cloud/v1alpha1",
			Kind:       "AdminKubeconfigRequest",
			Status: types.AdminKubeconfigResponseStatus{
				Kubeconfig:          base64.StdEncoding.EncodeToString([]byte("invalid-kubeconfig-data-for-testing")),
				ExpirationTimestamp: time.Now().Add(ttl).Format(time.RFC3339),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Generate kubeconfig
	kubeconfigStr, expTime, err := h.kubegen.GenerateShootKubeconfig(shoot.Metadata.Name, namespace, h.serverURL, ttl)
	if err != nil {
		errors.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := types.AdminKubeconfigResponse{
		APIVersion: "authentication.gardener.cloud/v1alpha1",
		Kind:       "AdminKubeconfigRequest",
		Status: types.AdminKubeconfigResponseStatus{
			Kubeconfig:          base64.StdEncoding.EncodeToString([]byte(kubeconfigStr)),
			ExpirationTimestamp: expTime.Format(time.RFC3339),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleAPIDiscovery(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"kind":     "APIVersions",
		"versions": []string{"v1"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleAPIsDiscovery(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"kind":       "APIGroupList",
		"apiVersion": "v1",
		"groups": []map[string]interface{}{
			{
				"name": "core.gardener.cloud",
				"versions": []map[string]string{
					{"groupVersion": "core.gardener.cloud/v1beta1", "version": "v1beta1"},
				},
				"preferredVersion": map[string]string{
					"groupVersion": "core.gardener.cloud/v1beta1",
					"version":      "v1beta1",
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleGardenerAPIResources(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"kind":         "APIResourceList",
		"apiVersion":   "v1",
		"groupVersion": "core.gardener.cloud/v1beta1",
		"resources": []map[string]interface{}{
			{
				"name":         "shoots",
				"singularName": "shoot",
				"namespaced":   true,
				"kind":         "Shoot",
				"verbs":        []string{"get", "list", "watch"},
				"shortNames":   []string{"sh"},
			},
			{
				"name":         "shoots/adminkubeconfig",
				"singularName": "",
				"namespaced":   true,
				"kind":         "AdminKubeconfigRequest",
				"verbs":        []string{"create"},
			},
			{
				"name":         "projects",
				"singularName": "project",
				"namespaced":   false,
				"kind":         "Project",
				"verbs":        []string{"get", "list"},
				"shortNames":   []string{"proj"},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleCoreAPIResources(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"kind":         "APIResourceList",
		"apiVersion":   "v1",
		"groupVersion": "v1",
		"resources": []map[string]interface{}{
			{
				"name":         "namespaces",
				"singularName": "namespace",
				"namespaced":   false,
				"kind":         "Namespace",
				"verbs":        []string{"get", "list"},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleProjects handles requests to the projects API
func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errors.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse path: /apis/core.gardener.cloud/v1beta1/projects[/{name}]
	path := strings.TrimPrefix(r.URL.Path, "/apis/core.gardener.cloud/v1beta1/projects")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		// List all projects
		h.handleListProjects(w, r)
	} else {
		// Get specific project
		h.handleGetProject(w, r, path)
	}
}

func (h *Handler) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects := h.store.ListProjects()

	items := make([]map[string]interface{}, 0, len(projects))
	for _, p := range projects {
		items = append(items, map[string]interface{}{
			"apiVersion": "core.gardener.cloud/v1beta1",
			"kind":       "Project",
			"metadata": map[string]interface{}{
				"name":              p.Name,
				"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"namespace": p.Namespace,
				"owner": map[string]interface{}{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "User",
					"name":     "system:serviceaccount:garden:gardener",
				},
				"createdBy": map[string]interface{}{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "User",
					"name":     "system:serviceaccount:garden:gardener",
				},
			},
			"status": map[string]interface{}{
				"phase":     "Ready",
				"namespace": p.Namespace,
			},
		})
	}

	resp := map[string]interface{}{
		"apiVersion": "core.gardener.cloud/v1beta1",
		"kind":       "ProjectList",
		"items":      items,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleGetProject(w http.ResponseWriter, r *http.Request, name string) {
	project, err := h.store.GetProject(name)
	if err != nil {
		errors.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	resp := map[string]interface{}{
		"apiVersion": "core.gardener.cloud/v1beta1",
		"kind":       "Project",
		"metadata": map[string]interface{}{
			"name":              project.Name,
			"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
		},
		"spec": map[string]interface{}{
			"namespace": project.Namespace,
			"owner": map[string]interface{}{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "User",
				"name":     "system:serviceaccount:garden:gardener",
			},
			"createdBy": map[string]interface{}{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "User",
				"name":     "system:serviceaccount:garden:gardener",
			},
		},
		"status": map[string]interface{}{
			"phase":     "Ready",
			"namespace": project.Namespace,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleListNamespaces handles GET /api/v1/namespaces
func (h *Handler) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errors.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get unique namespaces from shoots
	namespaces := h.store.ListNamespaces()

	items := make([]map[string]interface{}, 0, len(namespaces))
	for _, ns := range namespaces {
		items = append(items, map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": ns,
			},
		})
	}

	resp := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "NamespaceList",
		"metadata":   map[string]interface{}{},
		"items":      items,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
