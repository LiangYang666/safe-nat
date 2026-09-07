package tlsx

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"
)

func TestEnsureServerCertGeneratesAndReloads(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")

	c1, err := EnsureServerCert(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent reload must return the same keypair (stable fingerprint).
	c2, err := EnsureServerCert(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	fp1 := fingerprintOf(t, c1)
	fp2 := fingerprintOf(t, c2)
	if fp1 != fp2 {
		t.Fatalf("reload changed fingerprint: %s vs %s", fp1, fp2)
	}
}

func TestEnsureServerCertRequiresPaths(t *testing.T) {
	if _, err := EnsureServerCert("", ""); err == nil {
		t.Fatal("expected error for empty paths")
	}
}

func TestFingerprintVerification(t *testing.T) {
	dir := t.TempDir()
	fpFile := filepath.Join(dir, "known_servers")
	serverCert, err := EnsureServerCert(filepath.Join(dir, "s.crt"), filepath.Join(dir, "s.key"))
	if err != nil {
		t.Fatal(err)
	}
	serverFP := fingerprintOf(t, serverCert)
	serverCfg := ServerConfig(serverCert)

	// Round 1: first contact -> auto-trusted.
	clientCfg, err := ClientConfig(fpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHandshake(clientCfg, serverCfg); err != nil {
		t.Fatalf("first contact should be trusted: %v", err)
	}

	// Store now contains the fingerprint.
	stored, err := TrustedFingerprintFromFile(fpFile)
	if err != nil {
		t.Fatal(err)
	}
	if stored != serverFP {
		t.Fatalf("stored fp = %s, want %s", stored, serverFP)
	}

	// Round 2: same server, fresh client config -> still trusted.
	clientCfg2, err := ClientConfig(fpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHandshake(clientCfg2, serverCfg); err != nil {
		t.Fatalf("known server should verify: %v", err)
	}

	// A replaced server (new keypair) must be rejected.
	newCert, err := EnsureServerCert(filepath.Join(dir, "s2.crt"), filepath.Join(dir, "s2.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHandshake(clientCfg2, ServerConfig(newCert)); err == nil {
		t.Fatal("changed server fingerprint must be rejected")
	}
}

func TestEmptyConfigDefaultsToEnabled(t *testing.T) {
	if !Enabled(nil) {
		t.Fatal("TLS should default to enabled")
	}
	off := false
	if Enabled(&off) {
		t.Fatal("explicit false must disable")
	}
	on := true
	if !Enabled(&on) {
		t.Fatal("explicit true must enable")
	}
}

func fingerprintOf(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return Fingerprint(leaf)
}

// verifyHandshake runs a real TLS handshake over 127.0.0.1 TCP (net.Pipe is
// synchronous and stalls TLS handshakes — see project notes), returning the
// client-side verification error (if any).
func verifyHandshake(clientCfg, serverCfg *tls.Config) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		ts := tls.Server(conn, serverCfg)
		serverDone <- ts.Handshake()
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return err
	}
	tc := tls.Client(conn, clientCfg)
	cerr := tc.Handshake()
	_ = tc.Close()
	serr := <-serverDone
	if cerr == nil {
		return serr // surface server-side failures too
	}
	return cerr
}
