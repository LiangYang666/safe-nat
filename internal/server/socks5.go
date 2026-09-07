package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SOCKS5 (RFC 1928) server-side handshake, spoken by the public server on a
// socks5-typed tunnel port. Only no-auth + CONNECT are supported: the parsed
// target is handed to the client over the control channel, which performs the
// actual dial (the target lives in the client's LAN). See design.md §3/M3.
const socksHandshakeTimeout = 10 * time.Second

var (
	errSocksVer  = errors.New("socks5: unsupported version")
	errSocksAuth = errors.New("socks5: no acceptable auth method")
	errSocksCmd  = errors.New("socks5: only CONNECT is supported")
	errSocksAddr = errors.New("socks5: bad address")
	errSocksPort = errors.New("socks5: bad port")
)

// handshakeSocks5 performs the full client greeting + CONNECT request read
// and returns the parsed target host/port. The peer is expected to close the
// connection on any returned error (failure replies are written when the
// protocol allows it).
func handshakeSocks5(c net.Conn) (host string, port uint16, err error) {
	_ = c.SetDeadline(time.Now().Add(socksHandshakeTimeout))

	// --- greeting: VER NMETHODS METHODS ---
	var hdr [2]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil {
		return "", 0, fmt.Errorf("socks5: read greeting: %w", err)
	}
	if hdr[0] != 5 {
		return "", 0, errSocksVer
	}
	methods := make([]byte, int(hdr[1]))
	if _, err = io.ReadFull(c, methods); err != nil {
		return "", 0, fmt.Errorf("socks5: read methods: %w", err)
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_ = writeSocks5MethodReply(c, 0xff)
		return "", 0, errSocksAuth
	}
	if err = writeSocks5MethodReply(c, 0x00); err != nil {
		return "", 0, err
	}

	// --- request: VER CMD RSV ATYP ADDR PORT ---
	var rh [4]byte
	if _, err = io.ReadFull(c, rh[:]); err != nil {
		return "", 0, fmt.Errorf("socks5: read request: %w", err)
	}
	if rh[0] != 5 {
		return "", 0, errSocksVer
	}
	if rh[1] != 0x01 { // only CONNECT
		// Drain the rest of the request first so the peer's write completes
		// before we reply and the connection is torn down.
		_ = discardSocksAddr(c, rh[3])
		_ = writeSocks5Reply(c, 0x07)
		return "", 0, fmt.Errorf("%w: cmd=0x%02x", errSocksCmd, rh[1])
	}
	switch rh[3] { // ATYP
	case 0x01: // IPv4
		var b [4]byte
		if _, err = io.ReadFull(c, b[:]); err != nil {
			return "", 0, fmt.Errorf("socks5: read ipv4: %w", err)
		}
		host = net.IP(b[:]).String()
	case 0x03: // domain name
		var lb [1]byte
		if _, err = io.ReadFull(c, lb[:]); err != nil {
			return "", 0, fmt.Errorf("socks5: read domain len: %w", err)
		}
		n := int(lb[0])
		if n == 0 || n > 253 {
			return "", 0, errSocksAddr
		}
		d := make([]byte, n)
		if _, err = io.ReadFull(c, d); err != nil {
			return "", 0, fmt.Errorf("socks5: read domain: %w", err)
		}
		host = string(d)
	case 0x04: // IPv6
		var b [16]byte
		if _, err = io.ReadFull(c, b[:]); err != nil {
			return "", 0, fmt.Errorf("socks5: read ipv6: %w", err)
		}
		host = net.IP(b[:]).String()
	default:
		_ = writeSocks5Reply(c, 0x08)
		return "", 0, fmt.Errorf("%w: atyp=%d", errSocksAddr, rh[3])
	}
	var pb [2]byte
	if _, err = io.ReadFull(c, pb[:]); err != nil {
		return "", 0, fmt.Errorf("socks5: read port: %w", err)
	}
	port = binary.BigEndian.Uint16(pb[:])
	if port == 0 {
		_ = writeSocks5Reply(c, 0x05)
		return "", 0, errSocksPort
	}
	_ = c.SetDeadline(time.Time{}) // handshake done; raw pumping from now on
	return host, port, nil
}

// discardSocksAddr consumes an ATYP-encoded address + port from c so an
// unsupported command leaves no unread request bytes behind.
func discardSocksAddr(c net.Conn, atyp byte) error {
	var err error
	switch atyp {
	case 0x01: // IPv4
		_, err = io.CopyN(io.Discard, c, 4)
	case 0x03: // domain name
		var lb [1]byte
		if _, err = io.ReadFull(c, lb[:]); err == nil {
			_, err = io.CopyN(io.Discard, c, int64(lb[0]))
		}
	case 0x04: // IPv6
		_, err = io.CopyN(io.Discard, c, 16)
	default:
		return errSocksAddr
	}
	if err != nil {
		return err
	}
	_, err = io.CopyN(io.Discard, c, 2) // port
	return err
}

// writeSocks5MethodReply answers the greeting: VER(5) + chosen method.
// This is exactly 2 bytes per RFC 1928 §3 — anything longer desynchronizes
// strict clients such as curl, which read the method reply as 2 bytes.
func writeSocks5MethodReply(c net.Conn, method byte) error {
	_, err := c.Write([]byte{5, method})
	return err
}

// writeSocks5Reply sends a fixed-shape CONNECT reply: VER REP RSV
// ATYP(1=IPv4) BND.ADDR(0.0.0.0) BND.PORT(0). rep 0 = success, others are
// error codes. This is the full 10-byte reply per RFC 1928 §6.
func writeSocks5Reply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

// joinTarget formats host:port for dialing/logging.
func joinTarget(host string, port uint16) string {
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}
