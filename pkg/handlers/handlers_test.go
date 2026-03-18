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

