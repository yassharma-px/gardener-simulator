package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/errors"
	"github.com/yassharma/gardener-simulator/pkg/store"
	"github.com/yassharma/gardener-simulator/pkg/types"
)

func newTestHandler(cfg types.ErrorInjectionConfig) *Handler {
	shootStore := store.NewShootStore()
	injector := errors.NewInjector(cfg)
	return NewHandler(shootStore, injector, nil, "https://localhost:8443", time.Hour)
}

func TestTokenRequest_ConnectionRefusedError(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{
		Enabled:               true,
		ConnectionRefusedRate: 1.0, // 100% connection refused
	})

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-1/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}

	// Check for connection refused message in response
	if !strings.Contains(w.Body.String(), "connection refused") {
		t.Errorf("expected 'connection refused' in body, got: %s", w.Body.String())
	}
}

func TestTokenRequest_ConnectionResetError(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{
		Enabled:             true,
		ConnectionResetRate: 1.0, // 100% connection reset
	})

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-1/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "connection reset by peer") {
		t.Errorf("expected 'connection reset by peer' in body, got: %s", w.Body.String())
	}
}

func TestTokenRequest_IOTimeoutError(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{
		Enabled:       true,
		IOTimeoutRate: 1.0, // 100% i/o timeout
	})

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-1/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "i/o timeout") {
		t.Errorf("expected 'i/o timeout' in body, got: %s", w.Body.String())
	}
}

func TestTokenRequest_TokenExpiredError(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{
		Enabled:               true,
		TokenExpiredErrorRate: 1.0, // 100% token expired
	})

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-1/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "token is expired") {
		t.Errorf("expected 'token is expired' in body, got: %s", w.Body.String())
	}
}

func TestTokenRequest_PerServiceAccountTokenExpiredBehavior(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Set per-SA behavior
	h.injector.SetServiceAccountKubeconfigBehavior("garden-test", "sa-failing", "token_expired_error")

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-failing/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "token is expired") {
		t.Errorf("expected 'token is expired' in body, got: %s", w.Body.String())
	}
}

func TestTokenRequest_PerServiceAccountInvalidBehavior(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Set per-SA behavior to return invalid token
	h.injector.SetServiceAccountKubeconfigBehavior("garden-test", "sa-invalid", "invalid")

	body := `{"spec":{"audiences":["test"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/sa-invalid/token", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Parse response and check token is invalid
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	status := resp["status"].(map[string]interface{})
	token := status["token"].(string)

	if token != "invalid-token-data-not-a-jwt" {
		t.Errorf("expected invalid token string, got: %s", token)
	}
}

// Test restricted token functionality - tokens from restricted SAs get 403 for shoots/projects
func TestRestrictedToken_ListShoots_Forbidden(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Mark a service account as restricted
	h.injector.SetRestrictedServiceAccount("garden-test", "restricted-sa", true)

	// First get a token for the restricted SA
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/restricted-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	if tokenW.Code != http.StatusOK {
		t.Fatalf("expected token request status 200, got %d: %s", tokenW.Code, tokenW.Body.String())
	}

	// Parse the token response
	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Try to list shoots with the restricted token
	shootsReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/shoots", nil)
	shootsReq.Header.Set("Authorization", "Bearer "+token)
	shootsW := httptest.NewRecorder()
	h.ServeHTTP(shootsW, shootsReq)

	if shootsW.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for restricted token listing shoots, got %d: %s", shootsW.Code, shootsW.Body.String())
	}

	if !strings.Contains(shootsW.Body.String(), "insufficient permissions") {
		t.Errorf("expected 'insufficient permissions' in body, got: %s", shootsW.Body.String())
	}
}

func TestRestrictedToken_GetShoot_Forbidden(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Add a shoot to get
	h.store.AddShoot("garden-test", types.ShootConfig{
		Name:      "test-shoot",
		SeedName:  "test-seed",
		CloudType: "aws",
	})

	// Mark a service account as restricted
	h.injector.SetRestrictedServiceAccount("garden-test", "restricted-sa", true)

	// Get a token for the restricted SA
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/restricted-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Try to get a shoot with the restricted token
	shootReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/namespaces/garden-test/shoots/test-shoot", nil)
	shootReq.Header.Set("Authorization", "Bearer "+token)
	shootW := httptest.NewRecorder()
	h.ServeHTTP(shootW, shootReq)

	if shootW.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for restricted token getting shoot, got %d: %s", shootW.Code, shootW.Body.String())
	}
}

func TestRestrictedToken_ListProjects_Forbidden(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Mark a service account as restricted
	h.injector.SetRestrictedServiceAccount("garden-test", "restricted-sa", true)

	// Get a token for the restricted SA
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/restricted-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Try to list projects with the restricted token
	projectsReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/projects", nil)
	projectsReq.Header.Set("Authorization", "Bearer "+token)
	projectsW := httptest.NewRecorder()
	h.ServeHTTP(projectsW, projectsReq)

	if projectsW.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for restricted token listing projects, got %d: %s", projectsW.Code, projectsW.Body.String())
	}
}

func TestRestrictedToken_GetProject_Forbidden(t *testing.T) {
	// Need to use a store loaded with projects
	cfg := &types.SimulatorConfig{
		Projects: []types.ProjectConfig{
			{Name: "test-project", Namespace: "garden-test-project"},
		},
	}
	shootStore := store.NewShootStore()
	shootStore.LoadFromConfig(cfg)

	injector := errors.NewInjector(types.ErrorInjectionConfig{Enabled: true})
	h := NewHandler(shootStore, injector, nil, "https://localhost:8443", time.Hour)

	// Mark a service account as restricted
	h.injector.SetRestrictedServiceAccount("garden-test", "restricted-sa", true)

	// Get a token for the restricted SA
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/restricted-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Try to get a project with the restricted token
	projectReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/projects/test-project", nil)
	projectReq.Header.Set("Authorization", "Bearer "+token)
	projectW := httptest.NewRecorder()
	h.ServeHTTP(projectW, projectReq)

	if projectW.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for restricted token getting project, got %d: %s", projectW.Code, projectW.Body.String())
	}
}

func TestUnrestrictedToken_ListShoots_Success(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Get a token for an unrestricted SA (no restriction set)
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/normal-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Try to list shoots with the unrestricted token - should work
	shootsReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/shoots", nil)
	shootsReq.Header.Set("Authorization", "Bearer "+token)
	shootsW := httptest.NewRecorder()
	h.ServeHTTP(shootsW, shootsReq)

	if shootsW.Code != http.StatusOK {
		t.Errorf("expected status 200 for unrestricted token, got %d: %s", shootsW.Code, shootsW.Body.String())
	}
}

func TestRestrictedToken_ClearRestrictions(t *testing.T) {
	h := newTestHandler(types.ErrorInjectionConfig{Enabled: true})

	// Mark as restricted then clear
	h.injector.SetRestrictedServiceAccount("garden-test", "restricted-sa", true)
	h.injector.ClearRestrictedServiceAccounts()

	// Get a token
	body := `{"spec":{"audiences":["test"]}}`
	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/garden-test/serviceaccounts/restricted-sa/token", strings.NewReader(body))
	tokenW := httptest.NewRecorder()
	h.ServeHTTP(tokenW, tokenReq)

	var tokenResp map[string]interface{}
	json.NewDecoder(tokenW.Body).Decode(&tokenResp)
	status := tokenResp["status"].(map[string]interface{})
	token := status["token"].(string)

	// Should be able to list shoots after clearing
	shootsReq := httptest.NewRequest(http.MethodGet, "/apis/core.gardener.cloud/v1beta1/shoots", nil)
	shootsReq.Header.Set("Authorization", "Bearer "+token)
	shootsW := httptest.NewRecorder()
	h.ServeHTTP(shootsW, shootsReq)

	if shootsW.Code != http.StatusOK {
		t.Errorf("expected status 200 after clearing restrictions, got %d: %s", shootsW.Code, shootsW.Body.String())
	}
}
