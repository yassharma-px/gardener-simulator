package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yassharma/gardener-simulator/pkg/certs"
	"github.com/yassharma/gardener-simulator/pkg/errors"
	"github.com/yassharma/gardener-simulator/pkg/handlers"
	"github.com/yassharma/gardener-simulator/pkg/kubeconfig"
	"github.com/yassharma/gardener-simulator/pkg/store"
	"github.com/yassharma/gardener-simulator/pkg/types"
	"gopkg.in/yaml.v3"
)

// Server is the Gardener simulator server
type Server struct {
	config     *types.SimulatorConfig
	store      *store.ShootStore
	injector   *errors.Injector
	kubegen    *kubeconfig.Generator
	certBundle *certs.CertBundle
	httpServer *http.Server
	mgmtServer *http.Server
}

// NewServer creates a new simulator server
func NewServer(cfg *types.SimulatorConfig) (*Server, error) {
	shootStore := store.NewShootStore()
	shootStore.LoadFromConfig(cfg)

	injector := errors.NewInjector(cfg.ErrorInjection)

	kubegen, err := kubeconfig.NewGenerator()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig generator: %w", err)
	}

	certDir := cfg.CertDir
	if certDir == "" {
		certDir = filepath.Join(os.TempDir(), "gardener-simulator-certs")
	}

	certBundle, err := certs.GenerateCerts(certDir, []string{"localhost", "127.0.0.1"})
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificates: %w", err)
	}

	return &Server{
		config:     cfg,
		store:      shootStore,
		injector:   injector,
		kubegen:    kubegen,
		certBundle: certBundle,
	}, nil
}

// Start starts the simulator server
func (s *Server) Start() error {
	port := s.config.Port
	if port == 0 {
		port = 8443
	}
	mgmtPort := port + 1

	serverURL := fmt.Sprintf("https://localhost:%d", port)

	ttl := s.config.KubeconfigTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}

	// Create Gardener API handler
	handler := handlers.NewHandler(s.store, s.injector, s.kubegen, serverURL, ttl)

	// Create management API handler with cert bundle for kubeconfig endpoint
	mgmtHandler := handlers.NewManagementHandler(s.store, s.injector, s.certBundle.CACert, serverURL)

	// Setup HTTP servers
	mux := http.NewServeMux()
	mux.Handle("/", handler)

	mgmtMux := http.NewServeMux()
	mgmtMux.Handle("/management/", mgmtHandler)
	// Add healthz at root for Kubernetes probes
	mgmtMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	s.mgmtServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", mgmtPort),
		Handler: mgmtMux,
	}

	// Generate and save kubeconfig for accessing the simulator
	if err := s.generateSimulatorKubeconfig(serverURL); err != nil {
		return fmt.Errorf("failed to generate simulator kubeconfig: %w", err)
	}

	// Start management server (HTTP)
	go func() {
		log.Printf("Management API listening on http://localhost:%d/management/", mgmtPort)
		if err := s.mgmtServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Management server error: %v", err)
		}
	}()

	// Start main server (HTTPS)
	log.Printf("Gardener Simulator listening on %s", serverURL)
	log.Printf("Kubeconfig written to: %s", filepath.Join(s.config.CertDir, "kubeconfig.yaml"))

	return s.httpServer.ListenAndServeTLS(s.certBundle.ServerCertPath, s.certBundle.ServerKeyPath)
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	if err := s.mgmtServer.Shutdown(ctx); err != nil {
		log.Printf("Management server shutdown error: %v", err)
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) generateSimulatorKubeconfig(serverURL string) error {
	kubeconfig := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []map[string]interface{}{
			{
				"name": "gardener-simulator",
				"cluster": map[string]interface{}{
					"server":                     serverURL,
					"certificate-authority-data": base64.StdEncoding.EncodeToString(s.certBundle.CACert),
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

	kubeconfigYAML, err := yaml.Marshal(kubeconfig)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(s.config.CertDir, "kubeconfig.yaml"), kubeconfigYAML, 0600)
}
