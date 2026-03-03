// Package envtest provides an envtest-based server that uses real Gardener CRDs.
package envtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	mrand "math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/yassharma/gardener-simulator/pkg/types"
)

// ErrorConfig holds error injection configuration
type ErrorConfig struct {
	mu               sync.RWMutex
	unauthorizedRate float64
	forbiddenRate    float64
	notFoundRate     float64
	serverErrorRate  float64
	timeoutRate      float64
	rateLimitRate    float64
}

// Server is an envtest-based server that uses real Gardener CRDs.
type Server struct {
	config      *types.SimulatorConfig
	testEnv     *envtest.Environment
	client      client.Client
	restCfg     *rest.Config
	certDir     string
	stopCh      chan struct{}
	errorConfig *ErrorConfig
}

// NewServer creates a new envtest-based server.
func NewServer(config *types.SimulatorConfig) *Server {
	return &Server{
		config:      config,
		stopCh:      make(chan struct{}),
		errorConfig: &ErrorConfig{},
	}
}

// Start starts the envtest server.
func (s *Server) Start(ctx context.Context) error {
	// Setup controller-runtime logger (required before using envtest)
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create scheme with Gardener and core Kubernetes types
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add corev1 scheme: %w", err)
	}
	if err := gardencorev1beta1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add gardener scheme: %w", err)
	}

	// Find CRDs directory
	crdPaths := getCRDPaths()

	// Setup envtest environment with CRDs
	s.testEnv = &envtest.Environment{
		Scheme:                   scheme,
		CRDDirectoryPaths:        crdPaths,
		ControlPlaneStartTimeout: 120 * time.Second,
		ControlPlaneStopTimeout:  60 * time.Second,
	}

	// Start envtest
	cfg, err := s.testEnv.Start()
	if err != nil {
		return fmt.Errorf("failed to start envtest: %w", err)
	}
	s.restCfg = cfg

	// Create client
	s.client, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Create initial resources
	if err := s.createInitialResources(ctx); err != nil {
		return fmt.Errorf("failed to create initial resources: %w", err)
	}

	// Generate certificates for external access
	if err := s.generateCertificates(); err != nil {
		return fmt.Errorf("failed to generate certificates: %w", err)
	}

	// Write kubeconfig file for kubectl access
	if err := s.writeKubeconfig(); err != nil {
		log.Printf("Warning: failed to write kubeconfig: %v", err)
	}

	// Start reverse proxy with error injection (blocks until server stops)
	s.startProxy(ctx)

	return nil
}

// Stop stops the envtest server.
func (s *Server) Stop() error {
	close(s.stopCh)
	if s.testEnv != nil {
		return s.testEnv.Stop()
	}
	return nil
}

// createInitialResources creates initial projects and shoots.
func (s *Server) createInitialResources(ctx context.Context) error {
	// Use config if provided, otherwise use defaults
	if s.config != nil && len(s.config.Projects) > 0 {
		for _, projCfg := range s.config.Projects {
			// Create Namespace for shoots first
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: projCfg.Namespace,
				},
			}
			if err := s.client.Create(ctx, ns); err != nil {
				return fmt.Errorf("failed to create namespace %s: %w", projCfg.Namespace, err)
			}

			// Create Project
			project := &gardencorev1beta1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: projCfg.Name,
				},
				Spec: gardencorev1beta1.ProjectSpec{
					Namespace: stringPtr(projCfg.Namespace),
				},
			}
			if err := s.client.Create(ctx, project); err != nil {
				return fmt.Errorf("failed to create project %s: %w", projCfg.Name, err)
			}

			// Create Shoots for this project
			for _, shootCfg := range projCfg.Shoots {
				shoot := &gardencorev1beta1.Shoot{
					ObjectMeta: metav1.ObjectMeta{
						Name:      shootCfg.Name,
						Namespace: projCfg.Namespace,
						Labels:    shootCfg.Labels,
					},
					Spec: gardencorev1beta1.ShootSpec{
						Kubernetes: gardencorev1beta1.Kubernetes{
							Version: "1.28.0",
						},
						Provider: gardencorev1beta1.Provider{
							Type: shootCfg.CloudType,
							Workers: []gardencorev1beta1.Worker{
								{
									Name:    "worker-0",
									Machine: gardencorev1beta1.Machine{Type: "m5.large"},
									Maximum: 3,
									Minimum: 1,
								},
							},
						},
					},
				}
				if err := s.client.Create(ctx, shoot); err != nil {
					return fmt.Errorf("failed to create shoot %s: %w", shootCfg.Name, err)
				}
			}
		}
		return nil
	}

	// Default: create 1 project with 10 shoots
	projectName := "project-0"
	namespace := "garden-project-0"

	// Create Namespace first
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	if err := s.client.Create(ctx, ns); err != nil {
		return fmt.Errorf("failed to create namespace %s: %w", namespace, err)
	}

	// Create Project
	project := &gardencorev1beta1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: projectName,
		},
		Spec: gardencorev1beta1.ProjectSpec{
			Namespace: stringPtr(namespace),
		},
	}
	if err := s.client.Create(ctx, project); err != nil {
		return fmt.Errorf("failed to create project %s: %w", projectName, err)
	}

	// Create Shoots
	for j := 0; j < 10; j++ {
		shootName := fmt.Sprintf("shoot-%d", j)
		shoot := &gardencorev1beta1.Shoot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      shootName,
				Namespace: namespace,
			},
			Spec: gardencorev1beta1.ShootSpec{
				Kubernetes: gardencorev1beta1.Kubernetes{
					Version: "1.28.0",
				},
				Provider: gardencorev1beta1.Provider{
					Type: "aws",
					Workers: []gardencorev1beta1.Worker{
						{
							Name:    "worker-0",
							Machine: gardencorev1beta1.Machine{Type: "m5.large"},
							Maximum: 3,
							Minimum: 1,
						},
					},
				},
			},
		}
		if err := s.client.Create(ctx, shoot); err != nil {
			return fmt.Errorf("failed to create shoot %s: %w", shootName, err)
		}
	}

	return nil
}

// generateCertificates generates TLS certificates for the proxy.
func (s *Server) generateCertificates() error {
	s.certDir = filepath.Join(os.TempDir(), "gardener-sim-certs")
	if err := os.MkdirAll(s.certDir, 0755); err != nil {
		return err
	}

	// Generate CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Gardener Simulator CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}

	// Generate server cert
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "gardener-simulator"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "gardener-simulator"},
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return err
	}

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return err
	}

	// Write cert and key
	certFile, err := os.Create(filepath.Join(s.certDir, "tls.crt"))
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER}); err != nil {
		return err
	}

	keyFile, err := os.Create(filepath.Join(s.certDir, "tls.key"))
	if err != nil {
		return err
	}
	defer keyFile.Close()
	keyBytes, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return err
	}

	return nil
}

// startProxy starts the reverse proxy with error injection.
func (s *Server) startProxy(ctx context.Context) {
	port := 8443
	if s.config != nil && s.config.Port > 0 {
		port = s.config.Port
	}

	// Start management API server on port+1
	go s.startManagementAPI(port + 1)

	// Create reverse proxy to envtest API server
	target, _ := url.Parse(s.restCfg.Host)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Configure proxy transport with envtest TLS - use envtest client certs for auth
	cert, err := tls.X509KeyPair(s.restCfg.CertData, s.restCfg.KeyData)
	if err != nil {
		log.Printf("Warning: failed to load client certs: %v", err)
	}
	proxy.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			Certificates:       []tls.Certificate{cert},
		},
	}

	// Wrap with adminkubeconfig handler, then error injection
	handler := s.errorInjectionMiddleware(s.adminKubeconfigMiddleware(proxy))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
	}

	go func() {
		<-s.stopCh
		_ = server.Shutdown(context.Background())
	}()

	certFile := filepath.Join(s.certDir, "tls.crt")
	keyFile := filepath.Join(s.certDir, "tls.key")

	log.Printf("Starting envtest proxy on port %d", port)
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
		log.Printf("Proxy server error: %v", err)
	}
}

// errorInjectionMiddleware wraps requests with error injection logic.
func (s *Server) errorInjectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorConfig.mu.RLock()
		unauthorizedRate := s.errorConfig.unauthorizedRate
		forbiddenRate := s.errorConfig.forbiddenRate
		notFoundRate := s.errorConfig.notFoundRate
		serverErrorRate := s.errorConfig.serverErrorRate
		timeoutRate := s.errorConfig.timeoutRate
		rateLimitRate := s.errorConfig.rateLimitRate
		s.errorConfig.mu.RUnlock()

		// Check each error type in order
		r2 := mrand.Float64()
		cumulative := 0.0

		cumulative += unauthorizedRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusUnauthorized, "Unauthorized: invalid or expired credentials")
			return
		}

		cumulative += forbiddenRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusForbidden, "Forbidden: insufficient permissions")
			return
		}

		cumulative += notFoundRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusNotFound, "Not Found: resource does not exist")
			return
		}

		cumulative += serverErrorRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusInternalServerError, "Internal Server Error: temporary failure")
			return
		}

		cumulative += timeoutRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusGatewayTimeout, "Gateway Timeout: request took too long")
			return
		}

		cumulative += rateLimitRate
		if r2 < cumulative {
			s.writeErrorResponse(w, http.StatusTooManyRequests, "Too Many Requests: API rate limit exceeded")
			return
		}

		// Pass through to the actual API server
		next.ServeHTTP(w, r)
	})
}

// writeErrorResponse writes a Kubernetes-style error response.
func (s *Server) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
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

// adminKubeconfigMiddleware intercepts AdminKubeconfigRequest subresource calls and returns mock kubeconfigs.
// Pattern: POST /apis/core.gardener.cloud/v1beta1/namespaces/{namespace}/shoots/{shootName}/adminkubeconfig
var adminKubeconfigPattern = regexp.MustCompile(`^/apis/core\.gardener\.cloud/v1beta1/namespaces/([^/]+)/shoots/([^/]+)/adminkubeconfig$`)

func (s *Server) adminKubeconfigMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			matches := adminKubeconfigPattern.FindStringSubmatch(r.URL.Path)
			if matches != nil {
				namespace := matches[1]
				shootName := matches[2]
				s.handleAdminKubeconfigRequest(w, r, namespace, shootName)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleAdminKubeconfigRequest handles the AdminKubeconfigRequest subresource.
func (s *Server) handleAdminKubeconfigRequest(w http.ResponseWriter, r *http.Request, namespace, shootName string) {
	// Read the request body (AdminKubeconfigRequest)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Parse the request to get expiration seconds
	var req struct {
		Spec struct {
			ExpirationSeconds *int64 `json:"expirationSeconds"`
		} `json:"spec"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			log.Printf("Failed to parse AdminKubeconfigRequest: %v", err)
		}
	}

	expiration := int64(3600) // Default 1 hour
	if req.Spec.ExpirationSeconds != nil {
		expiration = *req.Spec.ExpirationSeconds
	}

	// Verify the shoot exists
	shoot := &gardencorev1beta1.Shoot{}
	if err := s.client.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: shootName}, shoot); err != nil {
		s.writeErrorResponse(w, http.StatusNotFound, fmt.Sprintf("shoot %q not found in namespace %q", shootName, namespace))
		return
	}

	// Generate a mock kubeconfig for this shoot
	kubeconfig := s.generateMockShootKubeconfig(namespace, shootName)

	// Calculate expiration timestamp
	expirationTime := time.Now().Add(time.Duration(expiration) * time.Second)

	// Build the response in Gardener's AdminKubeconfigRequest format
	response := map[string]interface{}{
		"apiVersion": "authentication.gardener.cloud/v1alpha1",
		"kind":       "AdminKubeconfigRequest",
		"metadata": map[string]interface{}{
			"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
		},
		"spec": map[string]interface{}{
			"expirationSeconds": expiration,
		},
		"status": map[string]interface{}{
			"kubeconfig":          base64.StdEncoding.EncodeToString([]byte(kubeconfig)),
			"expirationTimestamp": expirationTime.UTC().Format(time.RFC3339),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// generateMockShootKubeconfig generates a mock kubeconfig for a shoot cluster.
// In a real Gardener setup, this would be a kubeconfig to access the actual shoot cluster.
// For the simulator, we return either a custom kubeconfig (if configured) or one pointing to the simulator.
func (s *Server) generateMockShootKubeconfig(namespace, shootName string) string {
	// If a custom kubeconfig path is configured, read and return that
	if s.config != nil && s.config.ShootKubeconfigPath != "" {
		kubeconfig, err := os.ReadFile(s.config.ShootKubeconfigPath)
		if err != nil {
			log.Printf("Failed to read custom kubeconfig from %s: %v, falling back to generated kubeconfig", s.config.ShootKubeconfigPath, err)
		} else {
			return string(kubeconfig)
		}
	}

	// Determine the server URL
	serverURL := s.getExternalServerURL()

	// Build kubeconfig YAML
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: Config\n")
	sb.WriteString("clusters:\n")
	sb.WriteString(fmt.Sprintf("- name: %s--%s\n", namespace, shootName))
	sb.WriteString("  cluster:\n")
	sb.WriteString(fmt.Sprintf("    server: %s\n", serverURL))
	sb.WriteString("    insecure-skip-tls-verify: true\n")
	sb.WriteString("contexts:\n")
	sb.WriteString(fmt.Sprintf("- name: %s--%s\n", namespace, shootName))
	sb.WriteString("  context:\n")
	sb.WriteString(fmt.Sprintf("    cluster: %s--%s\n", namespace, shootName))
	sb.WriteString(fmt.Sprintf("    user: %s--%s\n", namespace, shootName))
	sb.WriteString(fmt.Sprintf("current-context: %s--%s\n", namespace, shootName))
	sb.WriteString("users:\n")
	sb.WriteString(fmt.Sprintf("- name: %s--%s\n", namespace, shootName))
	sb.WriteString("  user:\n")
	// Use client cert from the envtest for authentication
	if len(s.restCfg.CertData) > 0 {
		sb.WriteString(fmt.Sprintf("    client-certificate-data: %s\n", base64.StdEncoding.EncodeToString(s.restCfg.CertData)))
	}
	if len(s.restCfg.KeyData) > 0 {
		sb.WriteString(fmt.Sprintf("    client-key-data: %s\n", base64.StdEncoding.EncodeToString(s.restCfg.KeyData)))
	}

	return sb.String()
}

// getExternalServerURL returns the external server URL for kubeconfigs.
// If ExternalServer is configured, use that. Otherwise, fall back to localhost.
func (s *Server) getExternalServerURL() string {
	if s.config != nil && s.config.ExternalServer != "" {
		return s.config.ExternalServer
	}
	// Default to localhost with the configured port
	port := 8443
	if s.config != nil && s.config.Port > 0 {
		port = s.config.Port
	}
	return fmt.Sprintf("https://localhost:%d", port)
}

// startManagementAPI starts the management API server.
func (s *Server) startManagementAPI(port int) {
	mux := http.NewServeMux()

	// Error configuration endpoints
	mux.HandleFunc("/management/errors", s.handleErrors)
	mux.HandleFunc("/management/kubeconfig", s.handleKubeconfig)
	mux.HandleFunc("/management/healthz", s.handleHealthz)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		<-s.stopCh
		_ = server.Shutdown(context.Background())
	}()

	log.Printf("Management API listening on http://localhost:%d/management/", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Management server error: %v", err)
	}
}

// ErrorConfigRequest is the request format for configuring errors.
type ErrorConfigRequest struct {
	UnauthorizedRate float64 `json:"unauthorizedRate"`
	ForbiddenRate    float64 `json:"forbiddenRate"`
	NotFoundRate     float64 `json:"notFoundRate"`
	ServerErrorRate  float64 `json:"serverErrorRate"`
	TimeoutRate      float64 `json:"timeoutRate"`
	RateLimitRate    float64 `json:"rateLimitRate"`
}

func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req ErrorConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.errorConfig.mu.Lock()
		s.errorConfig.unauthorizedRate = req.UnauthorizedRate
		s.errorConfig.forbiddenRate = req.ForbiddenRate
		s.errorConfig.notFoundRate = req.NotFoundRate
		s.errorConfig.serverErrorRate = req.ServerErrorRate
		s.errorConfig.timeoutRate = req.TimeoutRate
		s.errorConfig.rateLimitRate = req.RateLimitRate
		s.errorConfig.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "configured",
			"config": req,
		})
		return
	}

	if r.Method == http.MethodGet {
		s.errorConfig.mu.RLock()
		resp := ErrorConfigRequest{
			UnauthorizedRate: s.errorConfig.unauthorizedRate,
			ForbiddenRate:    s.errorConfig.forbiddenRate,
			NotFoundRate:     s.errorConfig.notFoundRate,
			ServerErrorRate:  s.errorConfig.serverErrorRate,
			TimeoutRate:      s.errorConfig.timeoutRate,
			RateLimitRate:    s.errorConfig.rateLimitRate,
		}
		s.errorConfig.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleKubeconfig(w http.ResponseWriter, r *http.Request) {
	port := 8443
	if s.config != nil && s.config.Port > 0 {
		port = s.config.Port
	}

	// Build server URL
	serverURL := fmt.Sprintf("https://localhost:%d", port)
	if override := r.URL.Query().Get("server"); override != "" {
		serverURL = override
	}

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: gardener-simulator
  cluster:
    server: %s
    insecure-skip-tls-verify: true
users:
- name: simulator-admin
  user:
    token: simulator-admin-token
contexts:
- name: gardener-simulator
  context:
    cluster: gardener-simulator
    user: simulator-admin
current-context: gardener-simulator
`, serverURL)

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Write([]byte(kubeconfig))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetClient returns the Kubernetes client.
func (s *Server) GetClient() client.Client {
	return s.client
}

// GetRESTConfig returns the REST config for the envtest API server.
func (s *Server) GetRESTConfig() *rest.Config {
	return s.restCfg
}

func stringPtr(s string) *string {
	return &s
}

// getCRDPaths returns the paths to CRD directories.
// It checks multiple locations in order:
// 1. GARDENER_CRD_PATH environment variable
// 2. ./crds (relative to current working directory)
// 3. /app/crds (Docker container path)
func getCRDPaths() []string {
	// Check environment variable first
	if path := os.Getenv("GARDENER_CRD_PATH"); path != "" {
		return []string{path}
	}

	// Try common paths
	paths := []string{"./crds", "/app/crds"}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return []string{p}
		}
	}

	// Fallback to ./crds even if it doesn't exist
	// envtest will report the error
	return []string{"./crds"}
}

// writeKubeconfig writes a kubeconfig file for kubectl access.
func (s *Server) writeKubeconfig() error {
	port := 8443
	if s.config != nil && s.config.Port > 0 {
		port = s.config.Port
	}

	kubeconfigPath := "/tmp/gardener-sim-kubeconfig"
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: https://localhost:%d
  name: gardener-simulator
contexts:
- context:
    cluster: gardener-simulator
    user: admin
  name: gardener-simulator
current-context: gardener-simulator
users:
- name: admin
  user:
    client-certificate-data: %s
    client-key-data: %s
`,
		port,
		base64Encode(s.restCfg.CertData),
		base64Encode(s.restCfg.KeyData),
	)

	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		return err
	}
	log.Printf("Kubeconfig written to %s", kubeconfigPath)
	log.Printf("Use: export KUBECONFIG=%s", kubeconfigPath)
	return nil
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
