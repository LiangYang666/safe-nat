package server

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LiangYang666/safe-nat/internal/protocol"
)

const (
	loginTimeout      = 15 * time.Second // login frame must arrive within this
	idleTimeout       = 60 * time.Second // drop session if nothing heard this long
	heartbeatInterval = 10 * time.Second // prove liveness to the client
	dataBufSize       = 32 << 10
)

// session is one connected client: its control connection, bound remote
// ports (listeners) and the active tunneled conns keyed by connID.
type session struct {
	srv  *Server
	conn net.Conn
	sw   *protocol.ConnWriter
	log  *slog.Logger

	remoteHost string // client's IP (no port), for labels/events
	name       string // optional label from Login
	joinedAt   time.Time

	mu     sync.Mutex
	conns  map[uint32]*pubConn // public conns by connID
	connID atomic.Uint32

	tunnels []*tunnel // bound listeners
	wg      sync.WaitGroup

	done      chan struct{}
	closeOnce sync.Once
}

// tunnel is one bound remote listener requested at login.
type tunnel struct {
	name       string
	typ        string // "tcp" | "socks5"
	remotePort uint16
	firewall   bool
	ln         net.Listener
	opened     atomic.Uint64 // lifetime public conns accepted
	blocked    atomic.Uint64 // lifetime firewall denials on this port
}

// pubConn is one accepted public connection, tagged with its tunnel and peer.
type pubConn struct {
	c   net.Conn
	tun *tunnel
	ip  netip.Addr
}

func newSession(s *Server, conn net.Conn) *session {
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	if host == "" {
		host = conn.RemoteAddr().String()
	}
	return &session{
		srv:        s,
		conn:       conn,
		sw:         protocol.NewConnWriter(conn),
		log:        s.log.With("client", conn.RemoteAddr().String()),
		remoteHost: host,
		conns:      make(map[uint32]*pubConn),
		done:       make(chan struct{}),
	}
}

// label is the UI-facing name of this session: the client-provided name when
// set, otherwise its address.
func (s *session) label() string {
	if s.name != "" {
		return s.name
	}
	return s.remoteHost
}

func (s *session) evBase(typ string) Event {
	e := newEvent(typ)
	e.Client = s.label()
	e.ClientIP = s.remoteHost
	return e
}

func (s *session) run() {
	defer s.conn.Close()
	br := bufio.NewReaderSize(s.conn, dataBufSize)

	if err := s.login(br); err != nil {
		s.log.Warn("login failed", "err", err)
		return
	}
	s.log.Info("client session ready", "client", s.label(), "tunnels", len(s.tunnels))

	// Heartbeat: keep proving liveness to the client.
	s.wg.Add(1)
	go s.heartbeatLoop()

	// Reader: incoming frames from the client.
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(idleTimeout))
		f, err := protocol.ReadFrame(br)
		if err != nil {
			s.log.Debug("control read ended", "err", err)
			break
		}
		switch f.Type {
		case protocol.TypeData:
			s.writePublic(f)
		case protocol.TypeClose:
			var msg protocol.Close
			if err := json.Unmarshal(f.Payload, &msg); err != nil {
				s.log.Debug("bad close frame", "err", err)
			}
			s.closeConn(f.ConnID)
		case protocol.TypeHeartbeat, protocol.TypeLogin, protocol.TypeLoginResp, protocol.TypeOpen:
			// login dup / heartbeat: nothing to do
		default:
			s.log.Warn("unknown frame type", "type", f.Type)
		}
	}
	s.shutdown()
	s.srv.removeSession(s)
	ev := s.evBase(EvClientDown)
	ev.Detail = fmt.Sprintf("%d tunnels", len(s.tunnels))
	s.srv.hub.Publish(ev)
	s.wg.Wait()
	s.log.Info("client session ended")
}

// login reads the Login frame, verifies the token and binds remote ports.
func (s *session) login(br *bufio.Reader) error {
	_ = s.conn.SetReadDeadline(time.Now().Add(loginTimeout))
	f, err := protocol.ReadFrame(br)
	if err != nil {
		return fmt.Errorf("no login frame: %w", err)
	}
	if f.Type != protocol.TypeLogin {
		return fmt.Errorf("first frame is %s, want login", protocol.TypeName(f.Type))
	}
	var req protocol.Login
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return fmt.Errorf("bad login payload: %w", err)
	}
	if !s.verifyToken(req.Token) {
		_ = s.sw.WriteMsg(protocol.TypeLoginResp, 0, protocol.LoginResp{OK: false, Message: "token mismatch"})
		return errors.New("token mismatch")
	}
	if len(req.Tunnels) == 0 {
		_ = s.sw.WriteMsg(protocol.TypeLoginResp, 0, protocol.LoginResp{OK: false, Message: "no tunnels requested"})
		return errors.New("no tunnels requested")
	}
	s.name = strings.TrimSpace(req.Name)
	s.joinedAt = time.Now()

	resp := protocol.LoginResp{OK: true, Results: make([]protocol.TunnelResult, 0, len(req.Tunnels))}
	seen := make(map[uint16]bool, len(req.Tunnels))
	for _, t := range req.Tunnels {
		if seen[t.RemotePort] {
			continue // duplicate in one login: bind once
		}
		seen[t.RemotePort] = true
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", t.RemotePort))
		if err != nil {
			resp.Results = append(resp.Results, protocol.TunnelResult{Name: t.Name, RemotePort: t.RemotePort, OK: false, Error: err.Error()})
			continue
		}
		typ := t.Type
		if typ == "" {
			typ = "tcp"
		}
		tun := &tunnel{name: t.Name, typ: typ, remotePort: t.RemotePort, firewall: t.Firewall, ln: ln}
		s.tunnels = append(s.tunnels, tun)
		s.wg.Add(1)
		go s.acceptLoop(tun)
		resp.Results = append(resp.Results, protocol.TunnelResult{Name: t.Name, RemotePort: t.RemotePort, OK: true})
	}
	if len(s.tunnels) == 0 {
		resp.OK = false
		resp.Message = "no remote ports could be bound"
	}
	if err := s.sw.WriteMsg(protocol.TypeLoginResp, 0, resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Message)
	}
	s.srv.addSession(s)
	ev := s.evBase(EvClientUp)
	ev.Detail = fmt.Sprintf("%d tunnels", len(s.tunnels))
	s.srv.hub.Publish(ev)
	return nil
}

func (s *session) verifyToken(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.srv.cfg.Token)) == 1
}

// acceptLoop accepts public connections on one remote port and, if the peer
// passes the firewall, registers them and asks the client to dial the LAN end.
func (s *session) acceptLoop(t *tunnel) {
	defer s.wg.Done()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			s.log.Warn("tunnel accept failed", "port", t.remotePort, "err", err)
			return
		}
		ip := peerIP(c.RemoteAddr())
		if !s.ipAllowed(ip, t) {
			t.blocked.Add(1)
			s.srv.blockedTotal.Add(1)
			ev := s.evBase(EvBlocked)
			ev.Tunnel, ev.TypeTunnel, ev.RemotePort, ev.IP = t.name, t.typ, t.remotePort, ip.String()
			s.srv.hub.Publish(ev)
			s.log.Info("firewall blocked", "ip", ip.String(), "port", t.remotePort)
			_ = c.Close()
			continue
		}
		if t.typ == "socks5" {
			// SOCKS5 tunnels speak their handshake first; the parsed target
			// is then forwarded to the client, which dials it in its LAN.
			s.wg.Add(1)
			go func() { defer s.wg.Done(); s.serveSocks5(c, t) }()
			continue
		}
		id, pc := s.openPubConn(t, c, ip)
		s.log.Info("tunnel conn open", "conn_id", id, "ip", ip.String(), "port", t.remotePort)
		if err := s.sw.WriteMsg(protocol.TypeOpen, id, protocol.Open{RemotePort: t.remotePort}); err != nil {
			s.closeConn(id)
			s.shutdown()
			return
		}
		s.wg.Add(1)
		go s.pump(pc, id)
	}
}

// openPubConn registers an accepted public conn: allocates its connID, tracks
// it in the session map, bumps counters and publishes the UI event.
func (s *session) openPubConn(t *tunnel, c net.Conn, ip netip.Addr) (uint32, *pubConn) {
	id := s.connID.Add(1)
	pc := &pubConn{c: c, tun: t, ip: ip}
	s.mu.Lock()
	s.conns[id] = pc
	s.mu.Unlock()
	t.opened.Add(1)
	ev := s.evBase(EvConnOpen)
	ev.Tunnel, ev.TypeTunnel, ev.RemotePort, ev.IP, ev.ConnID = t.name, t.typ, t.remotePort, ip.String(), id
	s.srv.hub.Publish(ev)
	return id, pc
}

// serveSocks5 handles one accepted SOCKS5 connection: handshake, then a
// dynamic Open to the client carrying the CONNECT target. It runs in its own
// goroutine already accounted on s.wg by acceptLoop (no wg bookkeeping here).
func (s *session) serveSocks5(c net.Conn, t *tunnel) {
	host, port, err := handshakeSocks5(c)
	if err != nil {
		s.log.Debug("socks5 handshake failed", "err", err, "from", c.RemoteAddr().String())
		_ = c.Close()
		return
	}
	id, pc := s.openPubConn(t, c, peerIP(c.RemoteAddr()))
	// Optimistic success reply: the LAN dial happens on the client and its
	// outcome is not known here; a failed dial shows up as an immediate
	// reset to the SOCKS5 user (acceptable for v1, see design.md).
	if err := writeSocks5Reply(c, 0x00); err != nil {
		s.teardownConn(id, "socks5 reply: "+err.Error())
		return
	}
	if err := s.sw.WriteMsg(protocol.TypeOpen, id, protocol.Open{TargetHost: host, TargetPort: port}); err != nil {
		s.teardownConn(id, "open send failed")
		s.shutdown()
		return
	}
	s.log.Info("socks5 target opened", "conn_id", id, "target", joinTarget(host, port))
	s.pumpCore(pc, id)
}

// ipAllowed is the accept-time firewall: enforced only when the whitelist
// store is live (web management enabled) and the tunnel opts in. Tunnels
// with firewall=false are open to any peer (design.md §4).
func (s *session) ipAllowed(ip netip.Addr, t *tunnel) bool {
	if !t.firewall {
		return true
	}
	return s.srv.AllowIP(ip)
}

// pump wraps pumpCore with the session WaitGroup accounting (dispatch form).
func (s *session) pump(pc *pubConn, id uint32) {
	defer s.wg.Done()
	s.pumpCore(pc, id)
}

// pumpCore streams one public conn's data into Data frames; on EOF it asks
// the client to close its LAN end. Runs inline when the caller already holds
// a WaitGroup slot (serveSocks5).
func (s *session) pumpCore(pc *pubConn, id uint32) {
	buf := make([]byte, dataBufSize)
	for {
		n, err := pc.c.Read(buf)
		if n > 0 {
			if werr := s.sw.Write(protocol.TypeData, id, buf[:n]); werr != nil {
				s.shutdown()
				return
			}
		}
		if err != nil {
			reason := "wan closed"
			if !errors.Is(err, io.EOF) {
				reason = err.Error()
			}
			s.teardownConn(id, reason)
			return
		}
	}
}

// writePublic relays a client Data frame to the matching public conn.
func (s *session) writePublic(f protocol.Frame) {
	s.mu.Lock()
	pc := s.conns[f.ConnID]
	s.mu.Unlock()
	if pc == nil {
		s.log.Debug("data for unknown conn", "conn_id", f.ConnID)
		return
	}
	if _, err := pc.c.Write(f.Payload); err != nil {
		s.teardownConn(f.ConnID, "wan write failed: "+err.Error())
	}
}

// teardownConn closes the public conn and, when it was still registered,
// notifies the client and the web UI.
func (s *session) teardownConn(id uint32, reason string) {
	s.mu.Lock()
	pc, ok := s.conns[id]
	if ok {
		delete(s.conns, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	_ = pc.c.Close()
	_ = s.sw.WriteMsg(protocol.TypeClose, id, protocol.Close{Reason: reason})
	ev := s.evBase(EvConnClose)
	ev.Tunnel, ev.TypeTunnel, ev.RemotePort, ev.IP, ev.ConnID, ev.Reason = pc.tun.name, pc.tun.typ, pc.tun.remotePort, pc.ip.String(), id, reason
	s.srv.hub.Publish(ev)
}

// closeConn removes and closes the conn; reports whether it was present.
// Used when the client initiated the close.
func (s *session) closeConn(id uint32) bool {
	s.mu.Lock()
	pc, ok := s.conns[id]
	if ok {
		delete(s.conns, id)
	}
	s.mu.Unlock()
	if ok {
		_ = pc.c.Close()
	}
	return ok
}

func (s *session) heartbeatLoop() {
	defer s.wg.Done()
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.sw.Write(protocol.TypeHeartbeat, 0, nil); err != nil {
				s.shutdown()
				return
			}
		}
	}
}

// shutdown tears everything down once; callers must not wait on s.wg here
// (a pump may call it), run() does the Wait.
func (s *session) shutdown() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close() // control conn: unblocks readers/writers
		for _, t := range s.tunnels {
			_ = t.ln.Close()
		}
		s.mu.Lock()
		for _, pc := range s.conns {
			_ = pc.c.Close()
		}
		s.mu.Unlock()
	})
}

func peerIP(addr net.Addr) netip.Addr {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr()
}
