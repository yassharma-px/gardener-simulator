package kubeconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"gopkg.in/yaml.v3"
)

// Generator generates kubeconfigs for simulated shoots
type Generator struct {
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	caCertPEM []byte
}

// NewGenerator creates a new kubeconfig generator with its own CA
func NewGenerator() (*Generator, error) {
	// Generate CA key
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	// Create CA certificate
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Gardener Simulator"},
			CommonName:   "Gardener Simulator CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	return &Generator{
		caCert:    caCert,
		caKey:     caKey,
		caCertPEM: caCertPEM,
	}, nil
}

// GenerateShootKubeconfig generates a kubeconfig for a shoot cluster
func (g *Generator) GenerateShootKubeconfig(shootName, namespace, serverURL string, ttl time.Duration) (string, time.Time, error) {
	// Generate client key
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate client key: %w", err)
	}

	expirationTime := time.Now().Add(ttl)

	// Create client certificate
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"system:masters"},
			CommonName:   fmt.Sprintf("system:admin:%s", shootName),
		},
		NotBefore:   time.Now(),
		NotAfter:    expirationTime,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, g.caCert, &clientKey.PublicKey, g.caKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create client certificate: %w", err)
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER})
	clientKeyDER, _ := x509.MarshalECPrivateKey(clientKey)
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER})

	// Build kubeconfig
	kubeconfig := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []map[string]interface{}{
			{
				"name": shootName,
				"cluster": map[string]interface{}{
					"server":                     serverURL,
					"certificate-authority-data": base64.StdEncoding.EncodeToString(g.caCertPEM),
				},
			},
		},
		"users": []map[string]interface{}{
			{
				"name": fmt.Sprintf("%s-admin", shootName),
				"user": map[string]interface{}{
					"client-certificate-data": base64.StdEncoding.EncodeToString(clientCertPEM),
					"client-key-data":         base64.StdEncoding.EncodeToString(clientKeyPEM),
				},
			},
		},
		"contexts": []map[string]interface{}{
			{
				"name": shootName,
				"context": map[string]interface{}{
					"cluster": shootName,
					"user":    fmt.Sprintf("%s-admin", shootName),
				},
			},
		},
		"current-context": shootName,
	}

	kubeconfigYAML, err := yaml.Marshal(kubeconfig)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to marshal kubeconfig: %w", err)
	}

	return string(kubeconfigYAML), expirationTime, nil
}

// GetCACertPEM returns the CA certificate in PEM format
func (g *Generator) GetCACertPEM() []byte {
	return g.caCertPEM
}

