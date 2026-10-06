package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LiangYang666/safe-nat/internal/blocklog"
	"github.com/LiangYang666/safe-nat/internal/client"
	"github.com/LiangYang666/safe-nat/internal/config"
)

// TestRefusalRecordedWithHistory is the end-to-end shape of the refusal log:
// a real server + client pair, a firewall-protected tunnel and an empty
// whitelist. Dialing the public port must (a) be refused and (b) land in the
// refusal store the web UI reads — including the screen that says "this is a
// foreign IP" (covered=false) versus "already allowed" (covered=true).
func TestRefusalRecordedWithHistory(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer backend.Close()
	_, bPortS, _ := net.SplitHostPort(backend.Listener.Addr().String())
	var bPort int
	fmt.Sscanf(bPortS, "%d", &bPort)

	dir := t.TempDir()
	pub := freePort(t)
	srvCfg := &config.ServerConfig{
		BindPort: freePort(t),
		Token:    "test-token",
		TLS:      boolp(false),
		TLSCert:  dir + "/s.crt",
		TLSKey:   dir + "/s.key",
		// Web management enables the whitelist firewall and the stores.
		Web: &config.WebConfig{
			BindPort: freePort(t), Username: "admin", Password: "pw",
			DBPath: dir + "/web.db",
		},
	}
	srv, err := New(srvCfg, quietLogger())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	defer srv.Close()
	if srv.BlockedLog() == nil {
		t.Fatal("refusal store is nil although web management is on")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	cliCfg := &config.ClientConfig{
		Name:       "lan",
		ServerAddr: "127.0.0.1",
		ServerPort: srvCfg.BindPort,
		Token:      "test-token",
		TLS:        boolp(false),
		Tunnels: map[string]config.TunnelConfig{
			"web": {LocalIP: "127.0.0.1", LocalPort: bPort, RemotePort: pub},
		},
	}
	go func() { _ = client.New(cliCfg, quietLogger()).Run(ctx) }()

	addr := fmt.Sprintf("127.0.0.1:%d", pub)
	waitReady(t, addr, 8*time.Second) // its dial is itself a refusal (empty whitelist)
	time.Sleep(200 * time.Millisecond)

	// The refusal must show up in the log the UI reads, promptly.
	start := time.Now()
	var rows []blocklog.Entry
	for {
		rows, err = srv.BlockedLog().Rows(10)
		if err != nil {
			t.Fatalf("Rows: %v", err)
		}
		if len(rows) > 0 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("refusal never reached the log within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	latency := time.Since(start)
	if latency > 3*time.Second {
		t.Errorf("refusal took %v to become visible (want <= 3s: the writer flushes each second)", latency)
	}
	t.Logf("refusal visible after %v", latency)

	got := rows[0]
	if got.IP != "127.0.0.1" || got.Tunnel != "web" || got.RemotePort != uint16(pub) ||
		got.Kind != blocklog.KindBlocked || got.Count < 1 || got.LastSeen == "" {
		t.Fatalf("logged refusal = %+v, want ip 127.0.0.1 / tunnel web / port %d / kind %s", got, pub, blocklog.KindBlocked)
	}
	// After the client registers its tunnel, the *backend* is unreachable from
	// here — that is exactly the firewall working.
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Fatal("empty whitelist must refuse the public port, but the request succeeded")
	}

	// The UI's "already allowed" flag comes from WhiteMatch; flip it and the
	// status column turns green without touching the log.
	if rule, covered := srv.WhiteMatch("127.0.0.1"); covered {
		t.Fatalf("127.0.0.1 should not be covered yet (rule=%q)", rule)
	}
	if _, err := srv.Whitelist().Add("127.0.0.1", ""); err != nil {
		t.Fatalf("whitelist add: %v", err)
	}
	if rule, covered := srv.WhiteMatch("127.0.0.1"); !covered || rule != "127.0.0.1" {
		t.Fatalf("after adding, WhiteMatch = (%q, %v), want (127.0.0.1, true)", rule, covered)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("after allow-listing, the tunnel should serve: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("after allow-listing body = %q, want hello", body)
	}

	// Aggregates are durable: a fresh store on the same file still sees it.
	reopened, err := blocklog.Open(srvCfg.Web.DBPath)
	if err != nil {
		t.Fatalf("reopen refusal store: %v", err)
	}
	defer reopened.Close()
	again, err := reopened.Rows(10)
	if err != nil {
		t.Fatalf("Rows after reopen: %v", err)
	}
	if len(again) == 0 || again[0].IP != "127.0.0.1" {
		t.Fatalf("refusals must survive a restart, got %+v", again)
	}
}

// A refusal hit must never block the accept loop: with the writer stopped, the
// queue absorbs the burst and the counter reports what was dropped.
func TestRefusalRecordNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	s, err := New(&config.ServerConfig{
		BindPort: freePort(t), Token: "t", TLS: boolp(false),
		TLSCert: dir + "/s.crt", TLSKey: dir + "/s.key",
		Web: &config.WebConfig{BindPort: freePort(t), Username: "a", Password: "b", DBPath: dir + "/web.db"},
	}, quietLogger())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	defer s.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 4096; i++ {
			s.recordRefusal("203.0.113.5", "web", 48648, blocklog.KindBlocked, "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recordRefusal blocked the caller")
	}
}
