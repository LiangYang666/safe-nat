// Package server implements the safe-nat public server: it accepts control
// connections from clients, binds their requested remote ports, enforces the
// whitelist firewall at accept time and pumps tunneled data over the single
// control connection (design.md §3).
//
// The package also hosts the shared management state — session registry,
// whitelist store, event hub and aggregated stats — that internal/webapi
// exposes over HTTP.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/throttle"
	"github.com/LiangYang666/safe-nat/internal/tlsx"
	"github.com/LiangYang666/safe-nat/internal/traffic"
	"github.com/LiangYang666/safe-nat/internal/whitelist"
)

// Server is the control-plane manager: it owns the control listener, the
// registry of live client sessions, the whitelist store (when web management
// is enabled) and the event hub consumed by the web UI.
type Server struct {
	cfg   *config.ServerConfig
	log   *slog.Logger
	store *whitelist.Store // non-nil iff cfg.Web != nil
	hub   *Hub
	start time.Time

	trafDB   *traffic.DB      // nil unless web management is on
	trafLive *traffic.Tracker // in-memory totals + 1s rates

	// ctlLimiter rate-limits client login attempts per source IP, so the
	// public control port survives token brute force.
	ctlLimiter *throttle.Limiter

	// tlsCfg non-nil means the control listener speaks TLS (v0.7 default).
	tlsCfg *tls.Config

	mu       sync.Mutex
	sessions map[*session]struct{}

	// pendingLogin caps how many not-yet-authenticated connections a single
	// IP may hold open at once (each would otherwise sit in the 15s login
	// read window, letting scanners pile up goroutines). Successful logins
	// leave this set; the count is per source IP.
	pendingLogin struct {
		mu sync.Mutex
		n  map[string]int
	}

	blockedTotal atomic.Uint64 // firewall denials across all tunnels, lifetime
}

// maxPendingLogins is the per-IP cap on connections still inside the login
// handshake. Real clients hold exactly one control connection and finish the
// handshake in milliseconds, so NAT users sharing an egress IP are never
// affected.
const maxPendingLogins = 5

// New builds the manager; when web management is configured it opens (or
// creates) the whitelist database.
func New(cfg *config.ServerConfig, log *slog.Logger) (*Server, error) {
	s := &Server{
		cfg:        cfg,
		log:        log,
		hub:        NewHub(),
		start:      time.Now(),
		ctlLimiter: throttle.New(),
		sessions:   make(map[*session]struct{}),
	}
	s.pendingLogin.n = make(map[string]int)
	if tlsx.Enabled(cfg.TLS) {
		cert, err := tlsx.EnsureServerCert(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		s.tlsCfg = tlsx.ServerConfig(cert)
		leaf, perr := x509.ParseCertificate(cert.Certificate[0])
		if perr == nil {
			log.Info("tls enabled", "fingerprint", tlsx.Fingerprint(leaf))
		}
	}
	if cfg.Web != nil {
		store, err := whitelist.Open(cfg.Web.DBPath)
		if err != nil {
			return nil, err
		}
		s.store = store
		log.Info("whitelist store opened", "db", cfg.Web.DBPath)
		trafDB, err := traffic.Open(cfg.Web.DBPath)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		s.trafDB = trafDB
		s.trafLive = traffic.NewTracker()
		log.Info("traffic store opened", "db", cfg.Web.DBPath)
	}
	return s, nil
}

// Close releases the whitelist and traffic databases.
func (s *Server) Close() {
	if s.store != nil {
		_ = s.store.Close()
	}
	if s.trafDB != nil {
		_ = s.trafDB.Close()
	}
}

// Config exposes the server config to consumers (e.g. webapi).
func (s *Server) Config() *config.ServerConfig { return s.cfg }

// Whitelist returns the whitelist store, or nil when web management is off.
func (s *Server) Whitelist() *whitelist.Store { return s.store }

// AllowIP is the firewall predicate consulted at accept time: with web
// management disabled every IP is allowed (LiangNat parity).
func (s *Server) AllowIP(ip netip.Addr) bool {
	if s.store == nil {
		return true
	}
	return s.store.Contains(ip)
}

// WhiteMatch reports whether ipStr is covered by a stored rule and, when it
// is, which rule covers it (UI "is my IP allowed" card).
func (s *Server) WhiteMatch(ipStr string) (string, bool) {
	if s.store == nil {
		return "", false
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return "", false
	}
	return s.store.Match(ip)
}

// ---------- traffic accounting (see internal/traffic) ----------

// RecordTraffic adds one closed connection's bytes to the tunnel's live
// totals and its calendar-day total. No-op when web management is off.
func (s *Server) RecordTraffic(tunnel string, up, down int64) {
	if s.trafDB == nil || s.trafLive == nil {
		return
	}
	s.trafLive.Add(tunnel, up, down)
	s.trafDB.Record(tunnel, traffic.Day(time.Now()), up, down)
}

// TrafficLive returns real-time per-tunnel rates and lifetime totals.
func (s *Server) TrafficLive() []traffic.LiveView {
	if s.trafLive == nil {
		return []traffic.LiveView{}
	}
	return s.trafLive.Live()
}

// TrafficDaily returns per-day per-tunnel totals for the last n days.
// tunnel "" means every tunnel.
func (s *Server) TrafficDaily(tunnel string, days int) ([]traffic.DailyRow, error) {
	if s.trafDB == nil {
		return []traffic.DailyRow{}, nil
	}
	return s.trafDB.Daily(tunnel, days)
}

// TrafficSeries returns a time series in the rolling window
// [now-days*24h, now]; tunnel "" sums across every tunnel.
func (s *Server) TrafficSeries(tunnel string, days int, bucket string) ([]traffic.SeriesRow, error) {
	if s.trafDB == nil {
		return []traffic.SeriesRow{}, nil
	}
	return s.trafDB.Series(tunnel, days, bucket)
}

// SubscribeEvents hands the caller the live event stream (web UI / SSE).
func (s *Server) SubscribeEvents() (<-chan Event, func()) {
	return s.hub.Subscribe()
}

// Publish emits one event to every web-UI subscriber. The web API uses it to
// surface login-failure attempts from its own HTTP layer (webapi package
// cannot see the session internals).
func (s *Server) Publish(ev Event) {
	s.hub.Publish(ev)
}

// pendingEnter reserves one login slot for ip; false means the cap is
// reached and the connection should be dropped without reading anything.
func (s *Server) pendingEnter(ip string) bool {
	s.pendingLogin.mu.Lock()
	defer s.pendingLogin.mu.Unlock()
	if s.pendingLogin.n[ip] >= maxPendingLogins {
		return false
	}
	s.pendingLogin.n[ip]++
	return true
}

// pendingLeave releases one login slot for ip (called when the connection
// ends, whether or not it authenticated).
func (s *Server) pendingLeave(ip string) {
	s.pendingLogin.mu.Lock()
	defer s.pendingLogin.mu.Unlock()
	if s.pendingLogin.n[ip] <= 1 {
		delete(s.pendingLogin.n, ip)
		return
	}
	s.pendingLogin.n[ip]--
}

// Run listens on cfg.BindPort and serves client sessions until ctx is
// cancelled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.cfg.BindPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", addr, err)
	}
	if s.tlsCfg != nil {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	s.log.Info("server listening", "addr", ln.Addr().String(), "web", s.cfg.Web != nil, "tls", s.tlsCfg != nil)

	// 1 Hz live-rate sampler for the traffic view.
	if s.trafLive != nil {
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					s.trafLive.Sample()
				}
			}
		}()
	}

	var sessions sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // shutting down
			}
			return fmt.Errorf("server: accept: %w", err)
		}
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if !s.pendingEnter(host) {
			// Per-IP login-concurrency cap: a flood of not-yet-logged-in
			// connections from one source gets dropped before it can
			// occupy the 15s login read window.
			s.log.Debug("too many pending logins from one IP, dropping", "ip", host)
			_ = conn.Close()
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer s.pendingLeave(host)
			newSession(s, conn).run()
		}()
	}
	sessions.Wait()
	s.log.Info("server stopped")
	return nil
}

// ---------- management views (consumed by internal/webapi) ----------

// SessionView summarizes one connected client for the UI.
type SessionView struct {
	Client    string `json:"client"`
	Addr      string `json:"addr"`
	Connected string `json:"connected"` // RFC3339 UTC
	Tunnels   int    `json:"tunnels"`
	Conns     int    `json:"conns"`
}

// TunnelView summarizes one live tunnel across the whole server.
type TunnelView struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	RemotePort uint16 `json:"remote_port"`
	Firewall   bool   `json:"firewall"`
	Client     string `json:"client"`
	ConnActive int    `json:"conn_active"`
	ConnTotal  uint64 `json:"conn_total"`
	Blocked    uint64 `json:"blocked"`
}

// StatsView is the aggregate dashboard state.
type StatsView struct {
	UptimeSec    int64  `json:"uptime_sec"`
	Clients      int    `json:"clients"`
	Tunnels      int    `json:"tunnels"`
	ConnActive   int    `json:"conn_active"`
	ConnTotal    uint64 `json:"conn_total"`
	BlockedTotal uint64 `json:"blocked_total"`
	Whitelist    int    `json:"whitelist_rules"`
}

func (s *Server) sessionsSnapshot() []*session {
	s.mu.Lock()
	out := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		out = append(out, sess)
	}
	s.mu.Unlock()
	return out
}

// Sessions lists connected clients.
func (s *Server) Sessions() []SessionView {
	snaps := s.sessionsSnapshot()
	views := make([]SessionView, 0, len(snaps))
	for _, sess := range snaps {
		sess.mu.Lock()
		tunnels := len(sess.tunnels)
		conns := len(sess.conns)
		connected := sess.joinedAt.Format(time.RFC3339)
		client, addr := sess.label(), sess.conn.RemoteAddr().String()
		sess.mu.Unlock()
		views = append(views, SessionView{Client: client, Addr: addr, Connected: connected, Tunnels: tunnels, Conns: conns})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Client < views[j].Client })
	return views
}

// Tunnels lists every live tunnel with per-tunnel counters.
func (s *Server) Tunnels() []TunnelView {
	snaps := s.sessionsSnapshot()
	var views []TunnelView
	for _, sess := range snaps {
		sess.mu.Lock()
		for _, t := range sess.tunnels {
			active := 0
			for _, pc := range sess.conns {
				if pc.tun == t {
					active++
				}
			}
			views = append(views, TunnelView{
				Name:       t.name,
				Type:       t.typ,
				RemotePort: t.remotePort,
				Firewall:   t.firewall,
				Client:     sess.label(),
				ConnActive: active,
				ConnTotal:  t.opened.Load(),
				Blocked:    t.blocked.Load(),
			})
		}
		sess.mu.Unlock()
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].RemotePort != views[j].RemotePort {
			return views[i].RemotePort < views[j].RemotePort
		}
		return views[i].Client < views[j].Client
	})
	return views
}

// Stats aggregates the dashboard counters.
func (s *Server) Stats() StatsView {
	st := StatsView{
		UptimeSec:    int64(time.Since(s.start).Seconds()),
		BlockedTotal: s.blockedTotal.Load(),
	}
	if s.store != nil {
		st.Whitelist = s.store.Count()
	}
	for _, t := range s.Tunnels() {
		st.Tunnels++
		st.ConnActive += t.ConnActive
		st.ConnTotal += t.ConnTotal
	}
	st.Clients = len(s.Sessions())
	return st
}

func (s *Server) addSession(sess *session) {
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}
