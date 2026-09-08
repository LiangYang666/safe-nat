// Package client implements the safe-nat LAN-side client: it keeps one
// control connection to the server, announces its tunnels at login, dials
// the local service when the server reports a public connection, and pumps
// bytes in both directions (design.md §3). It reconnects with backoff.
package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/protocol"
	"github.com/LiangYang666/safe-nat/internal/tlsx"
)

const (
	loginTimeout      = 15 * time.Second
	readTimeout       = 35 * time.Second // no frame this long => server considered dead
	heartbeatInterval = 10 * time.Second // liveness proof to the server
	serverDialTimeout = 10 * time.Second
	localDialTimeout  = 10 * time.Second
	backoffBase       = 1 * time.Second
	backoffMax        = 30 * time.Second
	stableSession     = 30 * time.Second // session lasting this long resets the backoff
	dataBufSize       = 32 << 10
)

// entry couples a tunnel name with its config; order is stable (sorted).
type entry struct {
	name string
	cfg  config.TunnelConfig
}

// Client holds static config and derived tunnel table.
type Client struct {
	cfg     *config.ClientConfig
	log     *slog.Logger
	entries []entry
	byPort  map[uint16]entry
	tlsCfg  *tls.Config // non-nil: wrap the control connection in TLS (v0.7)
	tlsErr  error       // tls.Config construction failure (fail fast in Run)

	stateMu  sync.Mutex
	up       bool
	since    time.Time
	lastErr  string
	attempts int64
}

func (c *Client) markUp() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.up = true
	c.since = time.Now()
	c.lastErr = ""
}

func (c *Client) markDown(err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.up = false
	if err != nil {
		c.lastErr = err.Error()
	}
	c.attempts++
}

// State returns a snapshot for the local admin endpoint (`safenat status`).
func (c *Client) State() map[string]any {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	st := map[string]any{
		"connected": c.up,
		"server":    net.JoinHostPort(c.cfg.ServerAddr, fmt.Sprintf("%d", c.cfg.ServerPort)),
		"name":      c.cfg.Name,
		"tls":       c.tlsCfg != nil,
		"tunnels":   len(c.entries) + boolInt(c.cfg.Socks5 != nil),
	}
	if c.since.IsZero() {
		st["connected_since"] = nil
	} else {
		st["connected_since"] = c.since.UTC().Format(time.RFC3339)
	}
	if c.lastErr != "" {
		st["last_error"] = c.lastErr
	}
	st["reconnect_attempts"] = c.attempts
	return st
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// New validates nothing (config.LoadClient did); it only builds lookups and,
// when TLS is enabled, the fingerprint-verifying client tls.Config.
func New(cfg *config.ClientConfig, log *slog.Logger) *Client {
	entries := make([]entry, 0, len(cfg.Tunnels))
	for name, t := range cfg.Tunnels {
		entries = append(entries, entry{name: name, cfg: t})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	byPort := make(map[uint16]entry, len(entries))
	for _, e := range entries {
		byPort[uint16(e.cfg.RemotePort)] = e
	}
	c := &Client{cfg: cfg, log: log, entries: entries, byPort: byPort}
	if tlsx.Enabled(cfg.TLS) {
		tc, err := tlsx.ClientConfig(cfg.TLSFingerprints)
		if err != nil {
			// Failing open here would silently run without transport
			// security; fail fast instead so the operator sees it.
			log.Error("tls client config failed", "err", err)
			c.tlsErr = err
		} else {
			c.tlsCfg = tc
		}
	}
	return c
}

// Run connects, and on any failure reconnects with exponential backoff
// until ctx is cancelled. A connection that lived long enough (stableSession)
// is treated as a healthy baseline, so the next failure restarts from the
// base delay instead of compounding a stale backoff.
func (c *Client) Run(ctx context.Context) error {
	if c.tlsErr != nil {
		return fmt.Errorf("client: %w", c.tlsErr)
	}
	delay := backoffBase
	for {
		if ctx.Err() != nil {
			return nil
		}
		start := time.Now()
		err := c.runOnce(ctx)
		if err != nil {
			c.markDown(err)
		}
		if ctx.Err() != nil {
			return nil
		}
		sleep, next := nextDelay(delay, time.Since(start))
		delay = next
		c.log.Warn("connection lost, reconnecting", "err", err, "in", sleep.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(sleep):
		}
	}
}

// nextDelay computes the sleep before the next reconnect attempt plus the
// base delay for the retry after that one. Jitter (±20%) desynchronizes
// several clients reconnecting at once after a server restart.
func nextDelay(cur, lived time.Duration) (sleep, next time.Duration) {
	if lived >= stableSession {
		cur = backoffBase
	}
	j := 0.8 + 0.4*rand.Float64() // [0.8, 1.2)
	sleep = time.Duration(float64(cur) * j)
	next = cur * 2
	if next > backoffMax {
		next = backoffMax
	}
	return sleep, next
}

// runOnce performs one full connect-login-pump cycle.
func (c *Client) runOnce(ctx context.Context) error {
	addr := net.JoinHostPort(c.cfg.ServerAddr, fmt.Sprintf("%d", c.cfg.ServerPort))
	conn, err := net.DialTimeout("tcp", addr, serverDialTimeout)
	if err != nil {
		return fmt.Errorf("dial server %s: %w", addr, err)
	}
	if c.tlsCfg != nil {
		tc := tls.Client(conn, c.tlsCfg)
		_ = conn.SetDeadline(time.Now().Add(serverDialTimeout)) // bound the handshake
		if err := tc.Handshake(); err != nil {
			_ = conn.Close()
			return fmt.Errorf("tls handshake with %s: %w", addr, err)
		}
		_ = conn.SetDeadline(time.Time{})
		conn = tc // from here on everything speaks over TLS
	}
	defer conn.Close()
	c.log.Info("connected to server", "server", addr, "tls", c.tlsCfg != nil)

	// Watch ctx: on SIGINT/SIGTERM close the control conn so the blocking
	// frame read below unblocks immediately. Without this, server heartbeats
	// keep refreshing the read deadline and systemd's stop would have to
	// SIGKILL us after its 90s timeout.
	ctxWatch := make(chan struct{})
	defer close(ctxWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-ctxWatch:
		}
	}()

	sw := protocol.NewConnWriter(conn)
	br := bufio.NewReaderSize(conn, dataBufSize)

	// 1) login
	req := protocol.Login{Token: c.cfg.Token, Name: c.cfg.Name}
	for _, e := range c.entries {
		req.Tunnels = append(req.Tunnels, protocol.Tunnel{
			Name:       e.name,
			Type:       e.cfg.Type,
			RemotePort: uint16(e.cfg.RemotePort),
			Firewall:   e.cfg.FirewallEnabled(),
		})
	}
	if c.cfg.Socks5 != nil {
		req.Tunnels = append(req.Tunnels, protocol.Tunnel{
			Name:       "socks5",
			Type:       "socks5",
			RemotePort: uint16(c.cfg.Socks5.RemotePort),
			Firewall:   c.cfg.Socks5.FirewallEnabled(),
		})
	}
	if err := sw.WriteMsg(protocol.TypeLogin, 0, req); err != nil {
		return fmt.Errorf("send login: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(loginTimeout))
	lf, err := protocol.ReadFrame(br)
	if err != nil {
		return fmt.Errorf("login response: %w", err)
	}
	if lf.Type != protocol.TypeLoginResp {
		return fmt.Errorf("expected login response, got %s", protocol.TypeName(lf.Type))
	}
	var resp protocol.LoginResp
	if err := json.Unmarshal(lf.Payload, &resp); err != nil {
		return fmt.Errorf("bad login response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("server rejected login: %s", resp.Message)
	}
	c.markUp() // authenticated: the session is up from the operator's view
	for _, r := range resp.Results {
		if r.OK {
			c.log.Info("tunnel ready", "name", r.Name, "remote_port", r.RemotePort)
		} else {
			c.log.Warn("tunnel failed", "name", r.Name, "remote_port", r.RemotePort, "err", r.Error)
		}
	}
	if len(resp.Results) == 0 {
		return errors.New("server reported no tunnels")
	}

	// 2) serve frames until the connection dies
	var (
		mu    sync.Mutex
		conns = make(map[uint32]net.Conn) // LAN conns by connID
		wg    sync.WaitGroup
		done  = make(chan struct{})
	)

	closeLAN := func(id uint32) bool {
		mu.Lock()
		lan, ok := conns[id]
		if ok {
			delete(conns, id)
		}
		mu.Unlock()
		if ok {
			_ = lan.Close()
		}
		return ok
	}

	// Heartbeat ticker: liveness proof to the server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := sw.Write(protocol.TypeHeartbeat, 0, nil); err != nil {
					_ = conn.Close() // fail fast; reader exits, runOnce returns
					return
				}
			}
		}
	}()

	// LAN pump: stream one local conn into Data frames.
	pump := func(lan net.Conn, id uint32) {
		defer wg.Done()
		buf := make([]byte, dataBufSize)
		for {
			n, err := lan.Read(buf)
			if n > 0 {
				if werr := sw.Write(protocol.TypeData, id, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				reason := "lan closed"
				if !errors.Is(err, io.EOF) {
					reason = err.Error()
				}
				if closeLAN(id) {
					_ = sw.WriteMsg(protocol.TypeClose, id, protocol.Close{Reason: reason})
				}
				return
			}
		}
	}

	// Reader loop (main): server-pushed frames.
	for {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		f, err := protocol.ReadFrame(br)
		if err != nil {
			break
		}
		switch f.Type {
		case protocol.TypeOpen: // public conn arrived: dial the LAN side
			var msg protocol.Open
			if err := json.Unmarshal(f.Payload, &msg); err != nil {
				c.log.Debug("bad open frame", "err", err)
				_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "bad open frame"})
				continue
			}
			var localAddr, name string
			if msg.TargetPort > 0 {
				// SOCKS5 service: the server parsed the CONNECT request and
				// asks us to dial wherever the proxy user pointed at.
				localAddr = net.JoinHostPort(msg.TargetHost, fmt.Sprintf("%d", msg.TargetPort))
				name = "socks5"
			} else {
				e, ok := c.byPort[msg.RemotePort]
				if !ok {
					c.log.Warn("open for unknown remote port", "remote_port", msg.RemotePort)
					_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "no local tunnel"})
					continue
				}
				localAddr = net.JoinHostPort(e.cfg.LocalIP, fmt.Sprintf("%d", e.cfg.LocalPort))
				name = e.name
			}
			lan, err := net.DialTimeout("tcp", localAddr, localDialTimeout)
			if err != nil {
				c.log.Warn("local dial failed", "name", name, "local", localAddr, "err", err)
				_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "local dial failed: " + err.Error()})
				continue
			}
			mu.Lock()
			conns[f.ConnID] = lan
			mu.Unlock()
			c.log.Info("local conn opened", "name", name, "local", localAddr, "conn_id", f.ConnID)
			wg.Add(1)
			go pump(lan, f.ConnID)
		case protocol.TypeData:
			mu.Lock()
			lan := conns[f.ConnID]
			mu.Unlock()
			if lan == nil {
				c.log.Debug("data for unknown conn", "conn_id", f.ConnID)
				continue
			}
			if _, err := lan.Write(f.Payload); err != nil {
				closeLAN(f.ConnID)
			}
		case protocol.TypeClose:
			var msg protocol.Close
			_ = json.Unmarshal(f.Payload, &msg)
			c.log.Debug("server closed conn", "conn_id", f.ConnID, "reason", msg.Reason)
			closeLAN(f.ConnID)
		case protocol.TypeHeartbeat, protocol.TypeLoginResp:
			// liveness only
		default:
			c.log.Warn("unknown frame type", "type", f.Type)
		}
	}

	// Teardown: stop heartbeat, kill control + LAN conns so pumps exit.
	close(done)
	mu.Lock()
	for _, lan := range conns {
		_ = lan.Close()
	}
	mu.Unlock()
	_ = conn.Close()
	wg.Wait()
	return errors.New("control connection closed")
}
