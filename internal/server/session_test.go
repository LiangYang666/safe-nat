package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LiangYang666/safe-nat/internal/client"
	"github.com/LiangYang666/safe-nat/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freePort grabs an ephemeral port and releases it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func boolp(b bool) *bool { return &b }

// TestPublicTLS is the end-to-end shape of the feature: real server + real
// client over the control protocol, a TLS-opted remote port and a plain one.
//   - https://127.0.0.1:<tlsPort> terminates at the server and arrives at the
//     plain HTTP backend as HTTP/1.1 (no app-side TLS needed)
//   - a plaintext request to the tls port gets torn down after a failed
//     handshake
//   - the non-opted port keeps proxying plain HTTP
//   - TunnelView carries the tls flag for the web UI
func TestPublicTLS(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	}))
	defer backend.Close()
	_, bPortS, _ := net.SplitHostPort(backend.Listener.Addr().String())
	var bPort int
	fmt.Sscanf(bPortS, "%d", &bPort)

	dir := t.TempDir()
	tlsPort := freePort(t)
	plainPort := freePort(t)

	srvCfg := &config.ServerConfig{
		BindPort: freePort(t),
		Token:    "test-token",
		TLS:      boolp(false),
		TLSCert:  dir + "/s.crt",
		TLSKey:   dir + "/s.key",
		PublicTLS: &config.PublicTLSConfig{
			Cert: dir + "/p.crt", Key: dir + "/p.key", Ports: []int{tlsPort},
		},
	}
	srv, err := New(srvCfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	cliCfg := &config.ClientConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: srvCfg.BindPort,
		Token:      "test-token",
		TLS:        boolp(false),
		Tunnels: map[string]config.TunnelConfig{
			"sec":  {LocalIP: "127.0.0.1", LocalPort: bPort, RemotePort: tlsPort, Firewall: boolp(false)},
			"open": {LocalIP: "127.0.0.1", LocalPort: bPort, RemotePort: plainPort, Firewall: boolp(false)},
		},
	}
	cli := client.New(cliCfg, quietLogger())
	go func() { _ = cli.Run(ctx) }()

	waitReady(t, fmt.Sprintf("127.0.0.1:%d", tlsPort), 8*time.Second)
	waitReady(t, fmt.Sprintf("127.0.0.1:%d", plainPort), 8*time.Second)
	// give the client's login/bind a beat beyond raw port reachability
	time.Sleep(300 * time.Millisecond)

	// 1) HTTPS through the TLS port → plain HTTP backend.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	hc := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := hc.Get(fmt.Sprintf("https://127.0.0.1:%d/", tlsPort))
	if err != nil {
		t.Fatalf("https via tls port: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "proto=HTTP/1.1" {
		t.Fatalf("tls port: status=%d body=%q", resp.StatusCode, body)
	}

	// 2) Plaintext into the TLS port must not reach the backend.
	ln, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tlsPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial tls port: %v", err)
	}
	defer ln.Close()
	_ = ln.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := ln.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := ln.Read(buf)
	if n > 0 {
		t.Fatalf("plaintext got bytes on tls port: %q", buf[:n])
	}

	// 3) Non-opted port still plain-proxies.
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/", plainPort))
	if err != nil {
		t.Fatalf("plain http via non-tls port: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != "proto=HTTP/1.1" {
		t.Fatalf("plain port body = %q", body2)
	}

	// 4) Views carry the flag.
	byPort := map[uint16]TunnelView{}
	for _, v := range srv.Tunnels() {
		byPort[v.RemotePort] = v
	}
	if v, ok := byPort[uint16(tlsPort)]; !ok || !v.TLS {
		t.Fatalf("tls port view = %+v, want TLS=true", v)
	}
	if v, ok := byPort[uint16(plainPort)]; !ok || v.TLS {
		t.Fatalf("plain port view = %+v, want TLS=false", v)
	}
}

func TestPublicTLSFor(t *testing.T) {
	dir := t.TempDir()
	s, err := New(&config.ServerConfig{
		BindPort: freePort(t),
		Token:    "t",
		TLS:      boolp(false),
		PublicTLS: &config.PublicTLSConfig{
			Cert: dir + "/p.crt", Key: dir + "/p.key", Ports: []int{48080},
		},
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.publicTLSFor(48080); !ok {
		t.Fatal("opted-in port not recognised")
	}
	if _, ok := s.publicTLSFor(48081); ok {
		t.Fatal("non-opted port wrongly recognised")
	}
}

func waitReady(t *testing.T, addr string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("tunnel at %s never became ready", addr)
}
