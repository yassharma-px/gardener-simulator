package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// CertBundle holds certificate and key paths
type CertBundle struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
	CACert         []byte
}

// GenerateCerts generates TLS certificates for the simulator server
func GenerateCerts(certDir string, hosts []string) (*CertBundle, error) {
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cert dir: %w", err)
	}

	// Generate CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

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

	// Generate server certificate
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization: []string{"Gardener Simulator"},
			CommonName:   "gardener-simulator",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Add hosts
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			serverTemplate.IPAddresses = append(serverTemplate.IPAddresses, ip)
		} else {
			serverTemplate.DNSNames = append(serverTemplate.DNSNames, host)
		}
	}
	// Always add localhost
	serverTemplate.IPAddresses = append(serverTemplate.IPAddresses, net.ParseIP("127.0.0.1"))
	serverTemplate.DNSNames = append(serverTemplate.DNSNames, "localhost")

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	// Write CA cert
	caCertPath := filepath.Join(certDir, "ca.crt")
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	if err := os.WriteFile(caCertPath, caCertPEM, 0644); err != nil {
		return nil, fmt.Errorf("failed to write CA cert: %w", err)
	}

	// Write server cert
	serverCertPath := filepath.Join(certDir, "server.crt")
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	if err := os.WriteFile(serverCertPath, serverCertPEM, 0644); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	// Write server key
	serverKeyPath := filepath.Join(certDir, "server.key")
	serverKeyDER, _ := x509.MarshalECPrivateKey(serverKey)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})
	if err := os.WriteFile(serverKeyPath, serverKeyPEM, 0600); err != nil {
		return nil, fmt.Errorf("failed to write server key: %w", err)
	}

	return &CertBundle{
		CACertPath:     caCertPath,
		ServerCertPath: serverCertPath,
		ServerKeyPath:  serverKeyPath,
		CACert:         caCertPEM,
	}, nil
}

