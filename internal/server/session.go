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

	mu      sync.Mutex
	conns   map[uint32]net.Conn // public conns by connID
	connID  atomic.Uint32
	blocked atomic.Uint64 // firewall denials, surfaced in M2 stats

	tunnels []*tunnel // bound listeners
	wg      sync.WaitGroup

	done      chan struct{}
	closeOnce sync.Once
}

type tunnel struct {
	name       string
	remotePort uint16
	firewall   bool
	ln         net.Listener
}

func newSession(s *Server, conn net.Conn) *session {
	return &session{
		srv:     s,
		conn:    conn,
		sw:      protocol.NewConnWriter(conn),
		log:     s.log.With("client", conn.RemoteAddr().String()),
		conns:   make(map[uint32]net.Conn),
		done:    make(chan struct{}),
		tunnels: nil,
	}
}

func (s *session) run() {
	defer s.conn.Close()
	br := bufio.NewReaderSize(s.conn, dataBufSize)

	if err := s.login(br); err != nil {
		s.log.Warn("login failed", "err", err)
		return
	}
	s.log.Info("client session ready", "tunnels", len(s.tunnels))

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
		tun := &tunnel{name: t.Name, remotePort: t.RemotePort, firewall: t.Firewall, ln: ln}
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
			s.blocked.Add(1)
			s.log.Info("firewall blocked", "ip", ip.String(), "port", t.remotePort)
			_ = c.Close()
			continue
		}
		id := s.connID.Add(1)
		s.mu.Lock()
		s.conns[id] = c
		s.mu.Unlock()
		s.log.Info("tunnel conn open", "conn_id", id, "ip", ip.String(), "port", t.remotePort)
		if err := s.sw.WriteMsg(protocol.TypeOpen, id, protocol.Open{RemotePort: t.remotePort}); err != nil {
			s.closeConn(id)
			s.shutdown()
			return
		}
		s.wg.Add(1)
		go s.pump(c, id)
	}
}

// ipAllowed is the accept-time firewall. M1: web management is not wired yet,
// so nothing is blocked (LiangNat parity when web is disabled). M2 replaces
// this with the whitelist store when cfg.Web != nil.
func (s *session) ipAllowed(ip netip.Addr, t *tunnel) bool {
	if s.srv.cfg.Web == nil {
		return true // firewall only exists with web management enabled
	}
	// TODO(M2): consult whitelist store when t.firewall is true.
	return true
}

// pump streams one public conn's data into Data frames; on EOF it asks the
// client to close its LAN end.
func (s *session) pump(c net.Conn, id uint32) {
	defer s.wg.Done()
	buf := make([]byte, dataBufSize)
	for {
		n, err := c.Read(buf)
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
			if s.closeConn(id) {
				_ = s.sw.WriteMsg(protocol.TypeClose, id, protocol.Close{Reason: reason})
			}
			return
		}
	}
}

// writePublic relays a client Data frame to the matching public conn.
func (s *session) writePublic(f protocol.Frame) {
	s.mu.Lock()
	c := s.conns[f.ConnID]
	s.mu.Unlock()
	if c == nil {
		s.log.Debug("data for unknown conn", "conn_id", f.ConnID)
		return
	}
	if _, err := c.Write(f.Payload); err != nil {
		s.closeConn(f.ConnID)
	}
}

// closeConn removes and closes the conn; reports whether it was present.
func (s *session) closeConn(id uint32) bool {
	s.mu.Lock()
	c, ok := s.conns[id]
	if ok {
		delete(s.conns, id)
	}
	s.mu.Unlock()
	if ok {
		_ = c.Close()
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
		for _, c := range s.conns {
			_ = c.Close()
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
