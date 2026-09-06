// Package client implements the safe-nat LAN-side client: it keeps one
// control connection to the server, announces its tunnels at login, dials
// the local service when the server reports a public connection, and pumps
// bytes in both directions (design.md §3). It reconnects with backoff.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/protocol"
)

const (
	loginTimeout      = 15 * time.Second
	readTimeout       = 35 * time.Second // no frame this long => server considered dead
	heartbeatInterval = 10 * time.Second // liveness proof to the server
	serverDialTimeout = 10 * time.Second
	localDialTimeout  = 10 * time.Second
	backoffBase       = 1 * time.Second
	backoffMax        = 30 * time.Second
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
}

// New validates nothing (config.LoadClient did); it only builds lookups.
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
	return &Client{cfg: cfg, log: log, entries: entries, byPort: byPort}
}

// Run connects, and on any failure reconnects with exponential backoff
// until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	delay := backoffBase
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		c.log.Warn("connection lost, reconnecting", "err", err, "in", delay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		delay *= 2
		if delay > backoffMax {
			delay = backoffMax
		}
	}
}

// runOnce performs one full connect-login-pump cycle.
func (c *Client) runOnce(ctx context.Context) error {
	addr := net.JoinHostPort(c.cfg.ServerAddr, fmt.Sprintf("%d", c.cfg.ServerPort))
	conn, err := net.DialTimeout("tcp", addr, serverDialTimeout)
	if err != nil {
		return fmt.Errorf("dial server %s: %w", addr, err)
	}
	defer conn.Close()
	c.log.Info("connected to server", "server", addr)

	sw := protocol.NewConnWriter(conn)
	br := bufio.NewReaderSize(conn, dataBufSize)

	// 1) login
	req := protocol.Login{Token: c.cfg.Token}
	for _, e := range c.entries {
		req.Tunnels = append(req.Tunnels, protocol.Tunnel{
			Name:       e.name,
			RemotePort: uint16(e.cfg.RemotePort),
			Firewall:   e.cfg.FirewallEnabled(),
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
		case protocol.TypeOpen: // public conn arrived: dial the local service
			var msg protocol.Open
			if err := json.Unmarshal(f.Payload, &msg); err != nil {
				c.log.Debug("bad open frame", "err", err)
				_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "bad open frame"})
				continue
			}
			e, ok := c.byPort[msg.RemotePort]
			if !ok {
				c.log.Warn("open for unknown remote port", "remote_port", msg.RemotePort)
				_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "no local tunnel"})
				continue
			}
			localAddr := net.JoinHostPort(e.cfg.LocalIP, fmt.Sprintf("%d", e.cfg.LocalPort))
			lan, err := net.DialTimeout("tcp", localAddr, localDialTimeout)
			if err != nil {
				c.log.Warn("local dial failed", "name", e.name, "local", localAddr, "err", err)
				_ = sw.WriteMsg(protocol.TypeClose, f.ConnID, protocol.Close{Reason: "local dial failed: " + err.Error()})
				continue
			}
			mu.Lock()
			conns[f.ConnID] = lan
			mu.Unlock()
			c.log.Info("local conn opened", "name", e.name, "local", localAddr, "conn_id", f.ConnID)
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
