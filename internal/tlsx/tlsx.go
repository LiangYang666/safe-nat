// Package tlsx provides safe-nat's automatic transport encryption
// (design: tasks/20260907-safenat-v070).
//
// Model: the server generates a self-signed certificate on first start and
// persists it, so its fingerprint stays stable across restarts. The client
// trusts the server's fingerprint on first contact (TOFU, SSH known_hosts
// style) and verifies it on every later connection — no CA, no domain, no
// user configuration. What this buys: passive sniffing of the control
// channel (login token) and the data plane is defeated. What it does NOT
// buy: protection against an active MITM on the very first connection.
package tlsx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Enabled reports whether TLS is on (default true; tls: false disables).
func Enabled(v *bool) bool { return v == nil || *v }

// Fingerprint returns the hex SHA-256 of a certificate's DER bytes.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// EnsureServerCert loads the keypair from certPath/keyPath, generating and
// persisting a fresh self-signed ECDSA P-256 certificate (10y validity) when
// either file is missing. The private key is written 0600.
func EnsureServerCert(certPath, keyPath string) (tls.Certificate, error) {
	if certPath == "" || keyPath == "" {
		return tls.Certificate{}, fmt.Errorf("tlsx: tls enabled but cert_path/key_path not configured")
	}
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return tls.Certificate{}, fmt.Errorf("tlsx: load keypair: %w", err)
			}
			return cert, nil
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "safe-nat"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: create cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: mkdir cert dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: write key: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsx: load generated pair: %w", err)
	}
	return cert, nil
}

// ServerConfig builds the server-side tls.Config for the given cert.
func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}

// HandshakeTimeout bounds the TLS handshake on a public listener. Without
// it a slow-loris (open socket, dribble ClientHello bytes) would pin a
// goroutine and the connection forever: http.Server only timeouts cover the
// request phase, never the handshake.
const HandshakeTimeout = 10 * time.Second

// knownHosts is the client's trusted-server fingerprint store (SSH
// known_hosts analogue): one "host fingerprint-hex" line per trusted server.
type knownHosts struct {
	path    string
	mu      sync.Mutex
	trusted map[string]bool // fingerprint -> present
}

func loadKnownHosts(path string) (*knownHosts, error) {
	kh := &knownHosts{path: path, trusted: make(map[string]bool)}
	if path == "" {
		return kh, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return kh, nil // first run: nothing trusted yet
		}
		return nil, fmt.Errorf("tlsx: read known hosts: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept both the current single-field format ("<fingerprint>")
		// and any legacy "host <fingerprint>" lines: the fingerprint is
		// always the last whitespace-separated field.
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			kh.trusted[fields[len(fields)-1]] = true
		}
	}
	return kh, nil
}

// ClientConfig builds the client-side tls.Config that verifies the server's
// certificate fingerprint against the fpFile store.
func ClientConfig(fpFile string) (*tls.Config, error) {
	kh, err := loadKnownHosts(fpFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Identity is verified via VerifyPeerCertificate against the
		// known-hosts fingerprint; there is no CA to check against.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: kh.verifyPeer,
	}
	return cfg, nil
}

func (kh *knownHosts) verifyPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("tlsx: server sent no certificate")
	}
	sum := sha256.Sum256(rawCerts[0])
	fp := hex.EncodeToString(sum[:])
	return kh.check(fp)
}

// check verifies fp, auto-trusting on first-ever contact (TOFU). Once any
// server is trusted, an unknown fingerprint is rejected: for a single-server
// client that means the server's key changed (reinstall) or the connection
// is hijacked. Recovery: delete the fingerprint file (documented).
func (kh *knownHosts) check(fp string) error {
	kh.mu.Lock()
	defer kh.mu.Unlock()
	if kh.trusted[fp] {
		return nil
	}
	if len(kh.trusted) > 0 {
		return fmt.Errorf("tlsx: server fingerprint changed (got %s) — server replaced or connection hijacked; delete %s to trust the new server", fp, kh.path)
	}
	// First contact: trust and persist.
	kh.trusted[fp] = true
	if kh.path != "" {
		if err := os.MkdirAll(filepath.Dir(kh.path), 0o755); err == nil {
			f, err := os.OpenFile(kh.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err == nil {
				_, _ = f.WriteString(fp + "\n")
				_ = f.Close()
			}
		}
	}
	return nil
}

// TrustedFingerprint returns the first trusted fingerprint (status output).
func (kh *knownHosts) TrustedFingerprint() string {
	kh.mu.Lock()
	defer kh.mu.Unlock()
	for fp := range kh.trusted {
		return fp
	}
	return ""
}

// TrustedFingerprintFromFile loads a store read-only and returns its first
// fingerprint (used by `safenat status` without a live connection).
func TrustedFingerprintFromFile(path string) (string, error) {
	kh, err := loadKnownHosts(path)
	if err != nil {
		return "", err
	}
	return kh.TrustedFingerprint(), nil
}
