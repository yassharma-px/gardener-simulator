package types

import (
	"time"
)

// SimulatorConfig holds the configuration for the Gardener simulator
type SimulatorConfig struct {
	// Server configuration
	Port    int    `yaml:"port"`
	CertDir string `yaml:"certDir"`

	// ExternalServer is the external server URL for kubeconfigs returned to clients.
	// Example: "https://10.0.0.1:32443" or "https://gardener-sim.example.com:443"
	// If empty, defaults to "https://localhost:<port>"
	ExternalServer string `yaml:"externalServer"`

	// ShootKubeconfigPath is the path to a kubeconfig file that will be returned
	// for all shoot kubeconfig requests. If set, this kubeconfig is used instead
	// of generating one. This allows returning a real cluster's kubeconfig.
	ShootKubeconfigPath string `yaml:"shootKubeconfigPath"`

	// Projects to simulate
	Projects []ProjectConfig `yaml:"projects"`

	// Error injection settings
	ErrorInjection ErrorInjectionConfig `yaml:"errorInjection"`

	// Kubeconfig settings
	KubeconfigTTL time.Duration `yaml:"kubeconfigTTL"`
}

// ProjectConfig defines a Gardener project with its shoots
type ProjectConfig struct {
	Name      string        `yaml:"name"`
	Namespace string        `yaml:"namespace"` // garden-<project-name>
	Shoots    []ShootConfig `yaml:"shoots"`
}

// ShootConfig defines a Shoot cluster configuration
type ShootConfig struct {
	Name      string            `yaml:"name"`
	SeedName  string            `yaml:"seedName"`
	Labels    map[string]string `yaml:"labels"`
	CloudType string            `yaml:"cloudType"` // aws, gcp, azure
	Status    ShootStatus       `yaml:"status"`
	// For simulating shoots that disappear
	DeleteAfter *time.Duration `yaml:"deleteAfter,omitempty"`
}

// ShootStatus represents the status of a Shoot
type ShootStatus string

const (
	ShootStatusHealthy     ShootStatus = "Healthy"
	ShootStatusProgressing ShootStatus = "Progressing"
	ShootStatusUnhealthy   ShootStatus = "Unhealthy"
	ShootStatusHibernated  ShootStatus = "Hibernated"
)

// ErrorInjectionConfig configures error scenarios
type ErrorInjectionConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`

	// API-level errors
	ListShootsErrorRate      float64 `yaml:"listShootsErrorRate" json:"listShootsErrorRate"` // 0.0-1.0
	GetShootErrorRate        float64 `yaml:"getShootErrorRate" json:"getShootErrorRate"`
	AdminKubeconfigErrorRate float64 `yaml:"adminKubeconfigErrorRate" json:"adminKubeconfigErrorRate"`
	TokenRequestErrorRate    float64 `yaml:"tokenRequestErrorRate" json:"tokenRequestErrorRate"` // ServiceAccount TokenRequest errors

	// Specific error types
	AuthFailureRate         float64 `yaml:"authFailureRate" json:"authFailureRate"`                 // 401 errors
	ForbiddenRate           float64 `yaml:"forbiddenRate" json:"forbiddenRate"`                     // 403 errors
	NotFoundRate            float64 `yaml:"notFoundRate" json:"notFoundRate"`                       // 404 errors
	ServerErrorRate         float64 `yaml:"serverErrorRate" json:"serverErrorRate"`                 // 500 errors
	ServiceUnavailableRate  float64 `yaml:"serviceUnavailableRate" json:"serviceUnavailableRate"`   // 503 errors
	TimeoutRate             float64 `yaml:"timeoutRate" json:"timeoutRate"`                         // 504 errors
	RateLimitRate           float64 `yaml:"rateLimitRate" json:"rateLimitRate"`                     // 429 errors

	// Special response modes for kubeconfig testing
	InvalidKubeconfigRate float64 `yaml:"invalidKubeconfigRate" json:"invalidKubeconfigRate"` // Return malformed kubeconfig
	ExpiredKubeconfigRate float64 `yaml:"expiredKubeconfigRate" json:"expiredKubeconfigRate"` // Return already-expired kubeconfig

	// TTL override for testing rapid refresh scenarios
	// When > 0, overrides the requested TTL with this value (in seconds)
	ShortTTLSeconds int64 `yaml:"shortTTLSeconds" json:"shortTTLSeconds"`

	// Latency injection (in milliseconds)
	MinLatencyMs int `yaml:"minLatencyMs" json:"minLatencyMs"`
	MaxLatencyMs int `yaml:"maxLatencyMs" json:"maxLatencyMs"`

	// Per-shoot error injection (namespace/name -> always fail)
	FailingShoots map[string]int `yaml:"failingShoots" json:"failingShoots"` // shoot key -> error code

	// Per-shoot kubeconfig behavior (namespace/name -> behavior)
	// Values: "expired" (return expired kubeconfig), "invalid" (return malformed kubeconfig)
	ShootKubeconfigBehavior map[string]string `yaml:"shootKubeconfigBehavior" json:"shootKubeconfigBehavior"`

	// Per-ServiceAccount error injection (namespace/name -> error code)
	FailingServiceAccounts map[string]int `yaml:"failingServiceAccounts" json:"failingServiceAccounts"`
}

// Shoot represents a Gardener Shoot CR
type Shoot struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   ShootMetadata   `json:"metadata"`
	Spec       ShootSpec       `json:"spec"`
	Status     ShootStatusInfo `json:"status"`
}

// ShootMetadata contains Shoot metadata
type ShootMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp"`
}

// ShootSpec contains Shoot specification
type ShootSpec struct {
	SeedName     string           `json:"seedName"`
	Provider     ProviderSpec     `json:"provider"`
	Region       string           `json:"region"`
	CloudProfile string           `json:"cloudProfile,omitempty"`
	Kubernetes   *KubernetesSpec  `json:"kubernetes,omitempty"`
	Hibernation  *HibernationSpec `json:"hibernation,omitempty"`
}

// ProviderSpec defines the cloud provider
type ProviderSpec struct {
	Type string `json:"type"`
}

// KubernetesSpec defines Kubernetes configuration
type KubernetesSpec struct {
	Version string `json:"version"`
}

// HibernationSpec defines hibernation settings
type HibernationSpec struct {
	Enabled bool `json:"enabled"`
}

// ShootStatusInfo contains Shoot status information
type ShootStatusInfo struct {
	Conditions    []Condition    `json:"conditions,omitempty"`
	SeedName      string         `json:"seedName"`
	LastOperation *LastOperation `json:"lastOperation,omitempty"`
	Hibernated    bool           `json:"hibernated"`
}

// Condition represents a status condition
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// LastOperation describes the last operation on a shoot
type LastOperation struct {
	Type     string `json:"type"`     // Create, Reconcile, Delete
	State    string `json:"state"`    // Processing, Succeeded, Error, Failed
	Progress int    `json:"progress"` // 0-100
}

// ShootList is a list of Shoots
type ShootList struct {
	APIVersion string  `json:"apiVersion"`
	Kind       string  `json:"kind"`
	Items      []Shoot `json:"items"`
}

// AdminKubeconfigRequest is the request for admin kubeconfig
type AdminKubeconfigRequest struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       string                     `json:"kind"`
	Spec       AdminKubeconfigRequestSpec `json:"spec"`
}

// AdminKubeconfigRequestSpec contains the request spec
type AdminKubeconfigRequestSpec struct {
	ExpirationSeconds int64 `json:"expirationSeconds"`
}

// AdminKubeconfigResponse is the response with kubeconfig
type AdminKubeconfigResponse struct {
	APIVersion string                        `json:"apiVersion"`
	Kind       string                        `json:"kind"`
	Status     AdminKubeconfigResponseStatus `json:"status"`
}

// AdminKubeconfigResponseStatus contains the kubeconfig
type AdminKubeconfigResponseStatus struct {
	Kubeconfig          string `json:"kubeconfig"`
	ExpirationTimestamp string `json:"expirationTimestamp"`
}

// TokenRequest represents a Kubernetes TokenRequest for ServiceAccount tokens
// This matches the authentication.k8s.io/v1 TokenRequest resource
type TokenRequest struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Spec       TokenRequestSpec `json:"spec"`
}

// TokenRequestSpec contains the specification for a token request
type TokenRequestSpec struct {
	// Audiences are the intendend audiences of the token
	Audiences []string `json:"audiences,omitempty"`
	// ExpirationSeconds is the requested duration of validity of the token
	ExpirationSeconds *int64 `json:"expirationSeconds,omitempty"`
	// BoundObjectRef is a reference to an object that the token will be bound to
	BoundObjectRef *BoundObjectReference `json:"boundObjectRef,omitempty"`
}

// BoundObjectReference is a reference to an object that a token is bound to
type BoundObjectReference struct {
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
	Name       string `json:"name,omitempty"`
	UID        string `json:"uid,omitempty"`
}
