package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LiangYang666/safe-nat/internal/client"
	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/relay"
)

// shrinkRelay makes the relay policy small enough for a test to observe a
// stalled stream being reaped: a tiny per-stream queue and sub-second timeouts.
// Restored via t.Cleanup so the shared defaults are untouched elsewhere.
func shrinkRelay(t *testing.T) {
	t.Helper()
	q, w, s := relay.QueueLen, relay.WriteTimeout, relay.StallTimeout
	relay.QueueLen, relay.WriteTimeout, relay.StallTimeout = 8, 400*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { relay.QueueLen, relay.WriteTimeout, relay.StallTimeout = q, w, s })
}

// These two tests are the regression guard for the 2026-10-05 outage.
//
// Shape of the bug: both ends relayed stream data *inline* inside their single
// session read loop, and neither write had a deadline. One peer that stops
// reading (a visitor who walked away, a LAN service that stalls) therefore
// blocked that loop forever, which froze every other tunnel carried by the same
// client session — process alive, systemd `active`, panel still logging
// `tunnel conn open`, and zero bytes flowing anywhere.
//
// So: with a stalled stream in flight, a second tunnel on the same session must
// still answer, and the stalled stream must be reaped on its own.

// stallingBackend accepts connections and then pushes data forever without ever
// reading. Returns its port.
func stallingBackend(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 32<<10)
				for i := 0; i < 8192; i++ {
					_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// silentBackend accepts connections and never reads a byte, so the
// client→LAN direction backs up. Returns its port.
func silentBackend(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var open []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock() // hold it open, read nothing
			open = append(open, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range open {
			_ = c.Close()
		}
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// startTunnelPair brings up a real server + real client with two tunnels:
// "stall" → aPort, "good" → bPort. Both firewall-free (loopback visitor).
// Returns the two public ports and the server, so tests can inspect state.
func startTunnelPair(t *testing.T, aPort, bPort int) (stallPort, goodPort int, srv *Server) {
	t.Helper()
	stallPort, goodPort = freePort(t), freePort(t)
	dir := t.TempDir()
	srvCfg := &config.ServerConfig{
		BindPort: freePort(t),
		Token:    "wedge-token",
		TLS:      boolp(false),
		TLSCert:  dir + "/s.crt",
		TLSKey:   dir + "/s.key",
	}
	srv, err := New(srvCfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()

	cli := client.New(&config.ClientConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: srvCfg.BindPort,
		Token:      "wedge-token",
		TLS:        boolp(false),
		Tunnels: map[string]config.TunnelConfig{
			"stall": {LocalIP: "127.0.0.1", LocalPort: aPort, RemotePort: stallPort, Firewall: boolp(false)},
			"good":  {LocalIP: "127.0.0.1", LocalPort: bPort, RemotePort: goodPort, Firewall: boolp(false)},
		},
	}, quietLogger())
	go func() { _ = cli.Run(ctx) }()

	waitReady(t, fmt.Sprintf("127.0.0.1:%d", stallPort), 8*time.Second)
	waitReady(t, fmt.Sprintf("127.0.0.1:%d", goodPort), 8*time.Second)
	time.Sleep(300 * time.Millisecond)
	return stallPort, goodPort, srv
}

// goodBackend answers one plain HTTP request.
func goodBackend(t *testing.T) int {
	t.Helper()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(b.Close)
	return b.Listener.Addr().(*net.TCPAddr).Port
}

// mustAnswerWithin asserts the healthy tunnel still answers while a stalled
// stream is in flight on the same session.
func mustAnswerWithin(t *testing.T, goodPort int, d time.Duration) {
	t.Helper()
	hc := &http.Client{Timeout: d}
	resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/", goodPort))
	if err != nil {
		t.Fatalf("second tunnel on the same session went silent: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("second tunnel: status=%d body=%q", resp.StatusCode, body)
	}
}

// TestStalledVisitorDoesNotWedgeSession: a visitor that stops reading while the
// LAN side keeps pushing (server→visitor direction stalls).
func TestStalledVisitorDoesNotWedgeSession(t *testing.T) {
	shrinkRelay(t)
	stallPort, goodPort, _ := startTunnelPair(t, stallingBackend(t), goodBackend(t))

	// A visitor that connects and then never reads a byte.
	vis, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", stallPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer vis.Close()

	// Let the backend push until the visitor's socket buffers are full and the
	// relay would previously have wedged inside the session read loop.
	time.Sleep(700 * time.Millisecond)

	mustAnswerWithin(t, goodPort, 3*time.Second)

	// The stalled stream must be reaped by itself, not left hanging forever.
	deadline := time.Now().Add(5 * time.Second)
	_ = vis.SetReadDeadline(deadline)
	buf := make([]byte, 64<<10)
	for {
		if _, err := vis.Read(buf); err != nil {
			return // closed by the server: expected
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled visitor conn never got torn down")
		}
	}
}

// TestLanCloseFlushesQueue: the LAN service answers everything and closes at
// once (HTTP/1.0 shape). Every byte it sent must still reach the visitor.
// Moving data off the read loop introduced an ordering hazard: a
// client-initiated close used to arrive strictly after its data had been written
// inline, so closing the visitor socket there was safe. With a queue in between,
// closing on TypeClose cut off the tail (curl exit 18 — caught by the loopback
// e2e, not by the socket-level tests).
func TestLanCloseFlushesQueue(t *testing.T) {
	shrinkRelay(t) // QueueLen 8 => 256KiB, so a missing flush truncates obviously
	const body = 4 << 20

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 32<<10)
				for sent := 0; sent < body; sent += len(buf) {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	bPort := ln.Addr().(*net.TCPAddr).Port
	_, goodPort, _ := startTunnelPair(t, bPort, bPort)

	vis, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", goodPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer vis.Close()
	_ = vis.SetReadDeadline(time.Now().Add(15 * time.Second))
	n, err := io.Copy(io.Discard, vis)
	if err != nil {
		t.Fatalf("visitor read failed after %d of %d bytes: %v", n, body, err)
	}
	if n != body {
		t.Fatalf("transfer truncated: got %d of %d bytes", n, body)
	}
}

// TestStalledBackendDoesNotWedgeSession: the LAN backend stops reading while the
// visitor keeps sending (visitor→LAN direction stalls on the client side).
func TestStalledBackendDoesNotWedgeSession(t *testing.T) {
	shrinkRelay(t)
	stallPort, goodPort, _ := startTunnelPair(t, silentBackend(t), goodBackend(t))

	vis, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", stallPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer vis.Close()

	// Push until the client's write toward the silent backend has to block.
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		buf := make([]byte, 32<<10)
		for i := 0; i < 8192; i++ {
			_ = vis.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := vis.Write(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(700 * time.Millisecond)

	mustAnswerWithin(t, goodPort, 3*time.Second)
	_ = vis.Close()
	<-pushDone
}
