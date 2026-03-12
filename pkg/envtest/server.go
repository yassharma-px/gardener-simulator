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
	"strconv"
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
	mu                       sync.RWMutex
	enabled                  bool
	unauthorizedRate         float64
	forbiddenRate            float64
	notFoundRate             float64
	serverErrorRate          float64
	serviceUnavailableRate   float64
	timeoutRate              float64
	rateLimitRate            float64
	adminKubeconfigErrorRate float64
	tokenRequestErrorRate    float64
	failingShoots            map[string]int    // namespace/name -> error code
	failingServiceAccounts   map[string]int    // namespace/name -> error code

	// Kubeconfig injection settings
	invalidKubeconfigRate   float64
	expiredKubeconfigRate   float64
	shortTTLSeconds         int64
	shootKubeconfigBehavior map[string]string // namespace/name -> behavior ("expired", "invalid")
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
		config: config,
		stopCh: make(chan struct{}),
		errorConfig: &ErrorConfig{
			failingShoots:           make(map[string]int),
			failingServiceAccounts:  make(map[string]int),
			shootKubeconfigBehavior: make(map[string]string),
		},
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

	// Wrap with handlers, then error injection
	handler := s.errorInjectionMiddleware(s.tokenRequestMiddleware(s.adminKubeconfigMiddleware(proxy)))

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

// selectAdminKubeconfigErrorType selects an error type based on configured rates.
func (s *Server) selectAdminKubeconfigErrorType(rateLimitRate, serverErrorRate, timeoutRate float64) int {
	r := mrand.Float64()
	cumulative := 0.0

	// Check rate limit (429) first - this is the most important for testing backoff
	cumulative += rateLimitRate
	if r < cumulative {
		return http.StatusTooManyRequests
	}

	// Check server error (500)
	cumulative += serverErrorRate
	if r < cumulative {
		return http.StatusInternalServerError
	}

	// Check timeout (504)
	cumulative += timeoutRate
	if r < cumulative {
		return http.StatusGatewayTimeout
	}

	// Default to rate limit if nothing else specified
	return http.StatusTooManyRequests
}

// tokenRequestMiddleware intercepts ServiceAccount TokenRequest subresource calls.
// Pattern: POST /api/v1/namespaces/{namespace}/serviceaccounts/{name}/token
var tokenRequestPattern = regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/serviceaccounts/([^/]+)/token$`)

func (s *Server) tokenRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			matches := tokenRequestPattern.FindStringSubmatch(r.URL.Path)
			if matches != nil {
				namespace := matches[1]
				saName := matches[2]
				s.handleTokenRequest(w, r, namespace, saName)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
	// Check for per-shoot failure injection first
	s.errorConfig.mu.RLock()
	enabled := s.errorConfig.enabled
	key := namespace + "/" + shootName
	failCode, hasFail := s.errorConfig.failingShoots[key]
	adminKubeconfigErrorRate := s.errorConfig.adminKubeconfigErrorRate
	rateLimitRate := s.errorConfig.rateLimitRate
	serverErrorRate := s.errorConfig.serverErrorRate
	timeoutRate := s.errorConfig.timeoutRate
	invalidKubeconfigRate := s.errorConfig.invalidKubeconfigRate
	expiredKubeconfigRate := s.errorConfig.expiredKubeconfigRate
	shortTTLSeconds := s.errorConfig.shortTTLSeconds
	shootBehavior := s.errorConfig.shootKubeconfigBehavior[key]
	s.errorConfig.mu.RUnlock()

	// If per-shoot failure is configured, return that error
	if hasFail && failCode != 0 {
		s.writeErrorResponse(w, failCode, fmt.Sprintf("%s: shoot %s/%s", http.StatusText(failCode), namespace, shootName))
		return
	}

	// Check adminKubeconfigErrorRate if error injection is enabled
	if enabled && adminKubeconfigErrorRate > 0 {
		if mrand.Float64() < adminKubeconfigErrorRate {
			// Determine error type based on distribution
			errorCode := s.selectAdminKubeconfigErrorType(rateLimitRate, serverErrorRate, timeoutRate)
			s.writeErrorResponse(w, errorCode, fmt.Sprintf("Injected error for admin kubeconfig request: %s", http.StatusText(errorCode)))
			return
		}
	}

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

	expiration := int64(240) // Default 4 minutes
	if req.Spec.ExpirationSeconds != nil {
		log.Printf("Expiration seconds: %d", *req.Spec.ExpirationSeconds)
		expiration = *req.Spec.ExpirationSeconds
	}
	log.Printf("Expiration: %d", expiration)

	// Verify the shoot exists
	shoot := &gardencorev1beta1.Shoot{}
	if err := s.client.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: shootName}, shoot); err != nil {
		s.writeErrorResponse(w, http.StatusNotFound, fmt.Sprintf("shoot %q not found in namespace %q", shootName, namespace))
		return
	}

	// Check if we should return an invalid kubeconfig
	shouldReturnInvalid := shootBehavior == "invalid" || (enabled && invalidKubeconfigRate > 0 && mrand.Float64() < invalidKubeconfigRate)
	if shouldReturnInvalid {
		expirationTime := time.Now().Add(time.Duration(expiration) * time.Second)
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
				"kubeconfig":          base64.StdEncoding.EncodeToString([]byte("invalid-kubeconfig-data-for-testing")),
				"expirationTimestamp": expirationTime.UTC().Format(time.RFC3339),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(response)
		return
	}

	// Check if we should return an expired kubeconfig
	shouldReturnExpired := shootBehavior == "expired" || (enabled && expiredKubeconfigRate > 0 && mrand.Float64() < expiredKubeconfigRate)
	if shouldReturnExpired {
		kubeconfig := s.generateMockShootKubeconfig(namespace, shootName)
		expiredTime := time.Now().Add(-1 * time.Hour) // 1 hour ago
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
				"expirationTimestamp": expiredTime.UTC().Format(time.RFC3339),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(response)
		return
	}

	// Apply short TTL override if configured (for testing rapid refresh)
	if enabled && shortTTLSeconds > 0 {
		expiration = shortTTLSeconds
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

// handleTokenRequest handles the ServiceAccount TokenRequest subresource.
func (s *Server) handleTokenRequest(w http.ResponseWriter, r *http.Request, namespace, saName string) {
	// Check for per-ServiceAccount failure injection first
	s.errorConfig.mu.RLock()
	enabled := s.errorConfig.enabled
	key := namespace + "/" + saName
	failCode, hasFail := s.errorConfig.failingServiceAccounts[key]
	tokenRequestErrorRate := s.errorConfig.tokenRequestErrorRate
	rateLimitRate := s.errorConfig.rateLimitRate
	serverErrorRate := s.errorConfig.serverErrorRate
	timeoutRate := s.errorConfig.timeoutRate
	expiredKubeconfigRate := s.errorConfig.expiredKubeconfigRate
	shortTTLSeconds := s.errorConfig.shortTTLSeconds
	s.errorConfig.mu.RUnlock()

	// Return specific error for failing service accounts
	if hasFail && failCode != 0 {
		s.writeErrorResponse(w, failCode, fmt.Sprintf("%s: serviceaccount %s/%s", http.StatusText(failCode), namespace, saName))
		return
	}

	// Check tokenRequestErrorRate if error injection is enabled
	if enabled && tokenRequestErrorRate > 0 {
		if mrand.Float64() < tokenRequestErrorRate {
			// Determine error type based on distribution
			errorCode := s.selectAdminKubeconfigErrorType(rateLimitRate, serverErrorRate, timeoutRate)
			s.writeErrorResponse(w, errorCode, fmt.Sprintf("Injected error for token request: %s", http.StatusText(errorCode)))
			return
		}
	}

	// Read and parse the TokenRequest body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req struct {
		Spec struct {
			Audiences         []string `json:"audiences,omitempty"`
			ExpirationSeconds *int64   `json:"expirationSeconds,omitempty"`
			BoundObjectRef    *struct {
				Kind       string `json:"kind,omitempty"`
				APIVersion string `json:"apiVersion,omitempty"`
				Name       string `json:"name,omitempty"`
				UID        string `json:"uid,omitempty"`
			} `json:"boundObjectRef,omitempty"`
		} `json:"spec"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			log.Printf("Failed to parse TokenRequest: %v", err)
		}
	}

	// Default expiration: 1 hour (3600 seconds)
	expirationSeconds := int64(3600)
	if req.Spec.ExpirationSeconds != nil && *req.Spec.ExpirationSeconds > 0 {
		expirationSeconds = *req.Spec.ExpirationSeconds
	}

	// Check for short TTL override
	if shortTTLSeconds > 0 {
		expirationSeconds = shortTTLSeconds
	}

	now := time.Now()
	expirationTime := now.Add(time.Duration(expirationSeconds) * time.Second)

	// Check for expired token injection
	if enabled && expiredKubeconfigRate > 0 && mrand.Float64() < expiredKubeconfigRate {
		expirationTime = now.Add(-1 * time.Hour) // 1 hour ago
	}

	// Generate a mock JWT token
	token := s.generateMockJWT(namespace, saName, now, expirationTime)

	// Build TokenRequest response
	response := map[string]interface{}{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenRequest",
		"metadata": map[string]interface{}{
			"creationTimestamp": now.UTC().Format(time.RFC3339),
		},
		"spec": map[string]interface{}{
			"audiences":         req.Spec.Audiences,
			"expirationSeconds": expirationSeconds,
			"boundObjectRef":    req.Spec.BoundObjectRef,
		},
		"status": map[string]interface{}{
			"token":               token,
			"expirationTimestamp": expirationTime.UTC().Format(time.RFC3339),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// generateMockJWT creates a mock JWT token for testing purposes
func (s *Server) generateMockJWT(namespace, serviceAccount string, issuedAt, expiresAt time.Time) string {
	// Header (base64url encoded)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"mock-key"}`))

	// Payload (base64url encoded)
	payload := map[string]interface{}{
		"aud": []string{"https://kubernetes.default.svc"},
		"exp": expiresAt.Unix(),
		"iat": issuedAt.Unix(),
		"iss": "https://kubernetes.default.svc",
		"kubernetes.io": map[string]interface{}{
			"namespace": namespace,
			"serviceaccount": map[string]string{
				"name": serviceAccount,
				"uid":  "mock-uid-" + serviceAccount,
			},
		},
		"nbf": issuedAt.Unix(),
		"sub": fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
	}
	payloadJSON, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	// Signature (mock - just a placeholder)
	signature := base64.RawURLEncoding.EncodeToString([]byte("mock-signature-for-testing"))

	return header + "." + payloadB64 + "." + signature
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
	mux.HandleFunc("/management/errors/enable", s.handleEnableErrors)
	mux.HandleFunc("/management/errors/disable", s.handleDisableErrors)
	mux.HandleFunc("/management/errors/failing-shoots", s.handleClearFailingShoots)
	mux.HandleFunc("/management/errors/failing-serviceaccounts", s.handleClearFailingServiceAccounts)
	mux.HandleFunc("/management/errors/invalid-kubeconfig", s.handleSetInvalidKubeconfig)
	mux.HandleFunc("/management/errors/expired-kubeconfig", s.handleSetExpiredKubeconfig)
	mux.HandleFunc("/management/errors/short-ttl", s.handleSetShortTTL)
	mux.HandleFunc("/management/errors/kubeconfig-behaviors", s.handleClearKubeconfigBehaviors)
	mux.HandleFunc("/management/shoots/", s.handleShootEndpoints)
	mux.HandleFunc("/management/serviceaccounts/", s.handleServiceAccountEndpoints)
	mux.HandleFunc("/management/kubeconfig", s.handleKubeconfig)
	mux.HandleFunc("/management/kubeconfig/serviceaccount/", s.handleServiceAccountKubeconfig)
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
	Enabled                  bool    `json:"enabled"`
	UnauthorizedRate         float64 `json:"unauthorizedRate"`
	ForbiddenRate            float64 `json:"forbiddenRate"`
	NotFoundRate             float64 `json:"notFoundRate"`
	ServerErrorRate          float64 `json:"serverErrorRate"`
	ServiceUnavailableRate   float64 `json:"serviceUnavailableRate"`
	TimeoutRate              float64 `json:"timeoutRate"`
	RateLimitRate            float64 `json:"rateLimitRate"`
	AdminKubeconfigErrorRate float64 `json:"adminKubeconfigErrorRate"`
	TokenRequestErrorRate    float64 `json:"tokenRequestErrorRate"`
	InvalidKubeconfigRate    float64 `json:"invalidKubeconfigRate"`
	ExpiredKubeconfigRate    float64 `json:"expiredKubeconfigRate"`
	ShortTTLSeconds          int64   `json:"shortTTLSeconds"`
}

func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var req ErrorConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.errorConfig.mu.Lock()
		s.errorConfig.enabled = req.Enabled
		s.errorConfig.unauthorizedRate = req.UnauthorizedRate
		s.errorConfig.forbiddenRate = req.ForbiddenRate
		s.errorConfig.notFoundRate = req.NotFoundRate
		s.errorConfig.serverErrorRate = req.ServerErrorRate
		s.errorConfig.serviceUnavailableRate = req.ServiceUnavailableRate
		s.errorConfig.timeoutRate = req.TimeoutRate
		s.errorConfig.rateLimitRate = req.RateLimitRate
		s.errorConfig.adminKubeconfigErrorRate = req.AdminKubeconfigErrorRate
		s.errorConfig.tokenRequestErrorRate = req.TokenRequestErrorRate
		s.errorConfig.invalidKubeconfigRate = req.InvalidKubeconfigRate
		s.errorConfig.expiredKubeconfigRate = req.ExpiredKubeconfigRate
		s.errorConfig.shortTTLSeconds = req.ShortTTLSeconds
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
			Enabled:                  s.errorConfig.enabled,
			UnauthorizedRate:         s.errorConfig.unauthorizedRate,
			ForbiddenRate:            s.errorConfig.forbiddenRate,
			NotFoundRate:             s.errorConfig.notFoundRate,
			ServerErrorRate:          s.errorConfig.serverErrorRate,
			ServiceUnavailableRate:   s.errorConfig.serviceUnavailableRate,
			TimeoutRate:              s.errorConfig.timeoutRate,
			RateLimitRate:            s.errorConfig.rateLimitRate,
			AdminKubeconfigErrorRate: s.errorConfig.adminKubeconfigErrorRate,
			TokenRequestErrorRate:    s.errorConfig.tokenRequestErrorRate,
			InvalidKubeconfigRate:    s.errorConfig.invalidKubeconfigRate,
			ExpiredKubeconfigRate:    s.errorConfig.expiredKubeconfigRate,
			ShortTTLSeconds:          s.errorConfig.shortTTLSeconds,
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

// handleServiceAccountKubeconfig generates a kubeconfig using a token from the specified ServiceAccount.
// Path: /management/kubeconfig/serviceaccount/{namespace}/{name}
// Query params:
//   - server: override the API server URL
//   - expirationSeconds: token expiration (default: 3600)
func (s *Server) handleServiceAccountKubeconfig(w http.ResponseWriter, r *http.Request) {
	// Parse path: /management/kubeconfig/serviceaccount/{namespace}/{name}
	path := strings.TrimPrefix(r.URL.Path, "/management/kubeconfig/serviceaccount/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "invalid path, expected /management/kubeconfig/serviceaccount/{namespace}/{name}", http.StatusBadRequest)
		return
	}
	namespace, saName := parts[0], parts[1]

	// Build server URL
	port := 8443
	if s.config != nil && s.config.Port > 0 {
		port = s.config.Port
	}
	serverURL := fmt.Sprintf("https://localhost:%d", port)
	if s.config != nil && s.config.ExternalServer != "" {
		serverURL = s.config.ExternalServer
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

	// Check for short TTL override
	s.errorConfig.mu.RLock()
	shortTTLSeconds := s.errorConfig.shortTTLSeconds
	s.errorConfig.mu.RUnlock()
	if shortTTLSeconds > 0 {
		expirationSeconds = shortTTLSeconds
	}

	// Generate the token
	now := time.Now()
	expirationTime := now.Add(time.Duration(expirationSeconds) * time.Second)
	token := s.generateMockJWT(namespace, saName, now, expirationTime)

	// Build kubeconfig with the token
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: gardener-simulator
  cluster:
    server: %s
    insecure-skip-tls-verify: true
users:
- name: %s-%s
  user:
    token: %s
contexts:
- name: gardener-simulator
  context:
    cluster: gardener-simulator
    user: %s-%s
current-context: gardener-simulator
`, serverURL, namespace, saName, token, namespace, saName)

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=kubeconfig-%s-%s.yaml", namespace, saName))
	w.Write([]byte(kubeconfig))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleEnableErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.enabled = true
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

func (s *Server) handleDisableErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.enabled = false
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

func (s *Server) handleClearFailingShoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.failingShoots = make(map[string]int)
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-shoot failure injections cleared",
	})
}

func (s *Server) handleSetInvalidKubeconfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Rate float64 `json:"rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Rate < 0 || req.Rate > 1 {
		http.Error(w, "rate must be between 0.0 and 1.0", http.StatusBadRequest)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.invalidKubeconfigRate = req.Rate
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": fmt.Sprintf("invalid kubeconfig rate set to %.2f", req.Rate),
		"rate":    req.Rate,
	})
}

func (s *Server) handleSetExpiredKubeconfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Rate float64 `json:"rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Rate < 0 || req.Rate > 1 {
		http.Error(w, "rate must be between 0.0 and 1.0", http.StatusBadRequest)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.expiredKubeconfigRate = req.Rate
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "configured",
		"message": fmt.Sprintf("expired kubeconfig rate set to %.2f", req.Rate),
		"rate":    req.Rate,
	})
}

func (s *Server) handleSetShortTTL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Seconds int64 `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Seconds < 0 {
		http.Error(w, "seconds must be >= 0", http.StatusBadRequest)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.shortTTLSeconds = req.Seconds
	s.errorConfig.mu.Unlock()
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

func (s *Server) handleClearKubeconfigBehaviors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.shootKubeconfigBehavior = make(map[string]string)
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-shoot kubeconfig behaviors cleared",
	})
}

// SetShootFailureRequest configures a shoot to always fail
type SetShootFailureRequest struct {
	ErrorCode int `json:"errorCode"` // 0 to clear, or HTTP status code (401, 403, 404, 429, 500, etc.)
}

func (s *Server) handleShootEndpoints(w http.ResponseWriter, r *http.Request) {
	// Handle /management/shoots/{namespace}/{name}/fail or /kubeconfig-behavior
	path := strings.TrimPrefix(r.URL.Path, "/management/shoots/")

	if strings.HasSuffix(path, "/fail") {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path = strings.TrimSuffix(path, "/fail")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			http.Error(w, "invalid path, expected /management/shoots/{namespace}/{name}/fail", http.StatusBadRequest)
			return
		}
		namespace, name := parts[0], parts[1]

		var req SetShootFailureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.errorConfig.mu.Lock()
		key := namespace + "/" + name
		if req.ErrorCode == 0 {
			delete(s.errorConfig.failingShoots, key)
		} else {
			s.errorConfig.failingShoots[key] = req.ErrorCode
		}
		s.errorConfig.mu.Unlock()

		msg := fmt.Sprintf("shoot %s/%s will now return %d errors", namespace, name, req.ErrorCode)
		if req.ErrorCode == 0 {
			msg = fmt.Sprintf("shoot %s/%s failure injection cleared", namespace, name)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "configured",
			"message": msg,
		})
		return
	}

	if strings.HasSuffix(path, "/kubeconfig-behavior") {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path = strings.TrimSuffix(path, "/kubeconfig-behavior")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			http.Error(w, "invalid path, expected /management/shoots/{namespace}/{name}/kubeconfig-behavior", http.StatusBadRequest)
			return
		}
		namespace, name := parts[0], parts[1]

		var req struct {
			Behavior string `json:"behavior"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		validBehaviors := map[string]bool{"": true, "expired": true, "invalid": true}
		if !validBehaviors[req.Behavior] {
			http.Error(w, "behavior must be 'expired', 'invalid', or empty string to clear", http.StatusBadRequest)
			return
		}

		s.errorConfig.mu.Lock()
		key := namespace + "/" + name
		if req.Behavior == "" {
			delete(s.errorConfig.shootKubeconfigBehavior, key)
		} else {
			s.errorConfig.shootKubeconfigBehavior[key] = req.Behavior
		}
		s.errorConfig.mu.Unlock()

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
		return
	}

	http.Error(w, "endpoint not found", http.StatusNotFound)
}

func (s *Server) handleClearFailingServiceAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.errorConfig.mu.Lock()
	s.errorConfig.failingServiceAccounts = make(map[string]int)
	s.errorConfig.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "cleared",
		"message": "all per-ServiceAccount failure injections cleared",
	})
}

func (s *Server) handleServiceAccountEndpoints(w http.ResponseWriter, r *http.Request) {
	// Handle /management/serviceaccounts/{namespace}/{name}/fail
	path := strings.TrimPrefix(r.URL.Path, "/management/serviceaccounts/")

	if strings.HasSuffix(path, "/fail") {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path = strings.TrimSuffix(path, "/fail")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			http.Error(w, "invalid path, expected /management/serviceaccounts/{namespace}/{name}/fail", http.StatusBadRequest)
			return
		}
		namespace, name := parts[0], parts[1]

		var req struct {
			ErrorCode int `json:"errorCode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.errorConfig.mu.Lock()
		key := namespace + "/" + name
		if req.ErrorCode == 0 {
			delete(s.errorConfig.failingServiceAccounts, key)
		} else {
			s.errorConfig.failingServiceAccounts[key] = req.ErrorCode
		}
		s.errorConfig.mu.Unlock()

		msg := fmt.Sprintf("serviceaccount %s/%s will now return %d errors for TokenRequest", namespace, name, req.ErrorCode)
		if req.ErrorCode == 0 {
			msg = fmt.Sprintf("serviceaccount %s/%s TokenRequest failure injection cleared", namespace, name)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "configured",
			"message": msg,
		})
		return
	}

	http.Error(w, "endpoint not found", http.StatusNotFound)
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
