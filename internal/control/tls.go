package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
)

// LoadOrGenerateCert loads an existing TLS certificate pair from certFile and keyFile.
// If either file does not exist, it generates a self-signed ECDSA P-256 certificate,
// writes the PEM files to the specified paths, and returns the resulting certificate.
func LoadOrGenerateCert(certFile, keyFile string) (tls.Certificate, error) {
	// Both files must exist to load; otherwise generate a new pair.
	if fileExists(certFile) && fileExists(keyFile) {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	// Generate ECDSA P-256 private key.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Build self-signed certificate template.
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "reversproxy",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
		IsCA:        true,
	}

	// Self-sign: use the same key as both issuer and subject.
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Encode certificate to PEM.
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Encode private key to PEM.
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})

	// Write PEM files so that they can be reused on subsequent starts.
	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// NewServerTLSConfig returns a *tls.Config suitable for TLS 1.3 server use
// with the given certificate.
func NewServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}

// NewClientTLSConfig returns a *tls.Config suitable for TLS 1.3 client use.
// When insecureSkipVerify is true, the server's certificate is not verified —
// this is intended for development against self-signed certificates only.
func NewClientTLSConfig(insecureSkipVerify bool) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: insecureSkipVerify, //nolint:gosec // intentional dev flag
	}
}

// BuildClientTLSConfig builds the *tls.Config the server uses for its OUTBOUND
// control connections to clients. Security policy, in precedence order:
//  1. tls_fingerprint set → pin the client cert by exact SHA-256 match.
//  2. client_ca_cert set  → verify the client cert chains to the given CA.
//  3. insecure: true      → skip verification (development only; logged loudly).
//  4. otherwise           → verify against the system roots, which fails closed
//     for self-signed clients and forces the operator to configure (1), (2) or (3).
//
// This is the single source of truth shared by cmd/server and internal/app so
// the policy cannot drift between the two entrypoints.
func BuildClientTLSConfig(cfg *config.ServerConfig, log *slog.Logger) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}

	if fp := strings.TrimSpace(cfg.TLSFingerprint); fp != "" {
		want, err := parseCertFingerprint(fp)
		if err != nil {
			return nil, fmt.Errorf("invalid tls_fingerprint: %w", err)
		}
		// Exact pin: bypass the default chain check in favour of the fingerprint.
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // verification performed in VerifyConnection
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: client presented no certificate to pin")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], want) != 1 {
				return errors.New("tls: client certificate fingerprint mismatch")
			}
			return nil
		}
		return tlsCfg, nil
	}

	if cfg.ClientCACert != "" {
		caCert, err := os.ReadFile(cfg.ClientCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read client CA cert %q: %w", cfg.ClientCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse client CA cert %q: invalid PEM data", cfg.ClientCACert)
		}
		tlsCfg.RootCAs = pool
		return tlsCfg, nil
	}

	if cfg.Insecure {
		if log != nil {
			log.Warn("SECURITY: TLS certificate verification is DISABLED (insecure=true); the control channel and pre-shared token are exposed to machine-in-the-middle attacks — configure tls_fingerprint or client_ca_cert for production")
		}
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit, logged development escape hatch
		return tlsCfg, nil
	}

	// Fail-closed: verify against the system roots. Self-signed clients fail the
	// handshake until tls_fingerprint or client_ca_cert is configured.
	return tlsCfg, nil
}

// parseCertFingerprint decodes a hex SHA-256 fingerprint (optionally
// colon- or space-separated) into 32 raw bytes.
func parseCertFingerprint(s string) ([]byte, error) {
	clean := strings.NewReplacer(":", "", " ", "").Replace(s)
	b, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("not valid hex: %w", err)
	}
	if len(b) != sha256.Size {
		return nil, fmt.Errorf("expected %d hex bytes (SHA-256), got %d", sha256.Size, len(b))
	}
	return b, nil
}

// fileExists reports whether path names an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
