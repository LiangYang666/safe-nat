package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// pipePair returns a connected in-memory pair.
func pipePair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	c, s := net.Pipe()
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}

func connGreeting(methods ...byte) []byte {
	b := []byte{5, byte(len(methods))}
	return append(b, methods...)
}

func connRequest(atyp byte, addr []byte, port uint16) []byte {
	b := []byte{5, 1, 0, atyp}
	b = append(b, addr...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], port)
	return append(b, pb[:]...)
}

func readN(c net.Conn, n int) ([]byte, error) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func TestSocks5HandshakeDomain(t *testing.T) {
	peer, srv := pipePair(t)
	go func() {
		peer.Write(connGreeting(0x00))
		mb, err := readN(peer, 2) // method selection: VER + METHOD
		if err != nil || mb[0] != 5 || mb[1] != 0x00 {
			t.Errorf("bad method reply: %v %v", mb, err)
		}
		domain := "intranet.example"
		peer.Write(connRequest(0x03, append([]byte{byte(len(domain))}, domain...), 8080))
		peer.Close()
	}()
	host, port, err := handshakeSocks5(srv)
	if err != nil {
		t.Fatal(err)
	}
	if host != "intranet.example" || port != 8080 {
		t.Fatalf("got %s:%d, want intranet.example:8080", host, port)
	}
}

func TestSocks5MethodReplyIsTwoBytes(t *testing.T) {
	// RFC 1928 §3: the method-selection reply must be exactly 2 bytes.
	// A longer reply desynchronizes curl (it reads 2, then the leftover
	// bytes corrupt the CONNECT reply framing).
	peer, srv := pipePair(t)
	done := make(chan error, 1)
	go func() {
		peer.Write(connGreeting(0x00, 0x02))
		got, err := readN(peer, 2)
		if err != nil {
			done <- err
			return
		}
		if len(got) != 2 || got[0] != 5 || got[1] != 0x00 {
			done <- fmt.Errorf("method reply = %v, want exactly [5 0]", got)
			return
		}
		// Nothing else may be pending: with a 10-byte method reply (the old
		// bug) leftover junk would arrive right here and desync curl.
		_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		extra := make([]byte, 4)
		n, _ := peer.Read(extra)
		if n != 0 {
			done <- fmt.Errorf("unexpected extra bytes after method reply: %v", extra[:n])
			return
		}
		_ = peer.SetReadDeadline(time.Time{})
		// Complete the handshake so the server side returns normally.
		peer.Write(connRequest(0x01, []byte{1, 2, 3, 4}, 80))
		done <- nil
	}()
	host, port, err := handshakeSocks5(srv)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if host != "1.2.3.4" || port != 80 {
		t.Fatalf("got %s:%d", host, port)
	}
	peer.Close()
}

func TestSocks5HandshakeIPv4AndIPv6(t *testing.T) {
	for _, tc := range []struct {
		atyp byte
		addr []byte
		host string
	}{
		{0x01, []byte{192, 168, 1, 50}, "192.168.1.50"},
		{0x04, net.ParseIP("2001:db8::7").To16(), "2001:db8::7"},
	} {
		peer, srv := pipePair(t)
		go func() {
			peer.Write(connGreeting(0x00))
			_, _ = readN(peer, 2)
			peer.Write(connRequest(tc.atyp, tc.addr, 22))
			peer.Close()
		}()
		host, port, err := handshakeSocks5(srv)
		if err != nil {
			t.Fatalf("atyp %d: %v", tc.atyp, err)
		}
		if host != tc.host || port != 22 {
			t.Fatalf("got %s:%d, want %s:22", host, port, tc.host)
		}
	}
}

func TestSocks5RejectsNoAuthMethod(t *testing.T) {
	peer, srv := pipePair(t)
	go func() {
		peer.Write(connGreeting(0x02)) // only username/password offered
		mb, err := readN(peer, 2)
		if err != nil || mb[1] != 0xff {
			t.Errorf("want [5 0xff] method rejection, got %v %v", mb, err)
		}
		peer.Close()
	}()
	if _, _, err := handshakeSocks5(srv); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestSocks5RejectsNonConnect(t *testing.T) {
	peer, srv := pipePair(t)
	go func() {
		peer.Write(connGreeting(0x00))
		_, _ = readN(peer, 2)
		req := connRequest(0x01, []byte{1, 2, 3, 4}, 80)
		req[1] = 0x02 // BIND
		peer.Write(req)
		rb, err := readN(peer, 10) // full CONNECT reply shape
		if err != nil || rb[0] != 5 || rb[1] != 0x07 {
			t.Errorf("want [5 0x07] command rejection, got %v %v", rb, err)
		}
		peer.Close()
	}()
	if _, _, err := handshakeSocks5(srv); err == nil {
		t.Fatal("expected command error")
	}
}

func TestSocks5RejectsGarbageVersion(t *testing.T) {
	peer, srv := pipePair(t)
	go func() {
		peer.Write([]byte{4, 1, 0}) // SOCKS4-style
		peer.Close()
	}()
	if _, _, err := handshakeSocks5(srv); err == nil {
		t.Fatal("expected version error")
	}
}

func TestJoinTarget(t *testing.T) {
	if got := joinTarget("intranet.example", 8080); got != "intranet.example:8080" {
		t.Fatalf("got %s", got)
	}
	if got := joinTarget("2001:db8::5", 443); got != "[2001:db8::5]:443" {
		t.Fatalf("got %s", got)
	}
}
