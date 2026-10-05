// Package protocol defines the safe-nat wire protocol: a fixed 9-byte
// frame header (type | connID | length) on a single TCP control connection.
// connID 0 carries control messages (JSON payload); connID > 0 carries raw
// data frames of one tunneled connection.
//
// See design.md §3 (tasks/20260906-go-liangnat/design.md).
package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// HeaderSize is the fixed frame header size in bytes.
const HeaderSize = 9

// MaxFrameSize guards against malicious or corrupt length fields.
const MaxFrameSize = 8 << 20 // 8 MiB

// Frame types. 1 byte in the header.
const (
	TypeLogin     byte = 0x01 // c->s: login request (JSON Login)
	TypeLoginResp byte = 0x02 // s->c: login result (JSON LoginResp)
	TypeOpen      byte = 0x03 // s->c: public conn arrived, dial local (JSON Open)
	TypeClose     byte = 0x04 // either: close a tunneled conn (JSON Close)
	TypeData      byte = 0x05 // either: raw payload of a tunneled conn (connID>0)
	TypeHeartbeat byte = 0x06 // either: keepalive, empty payload, connID 0
)

// TypeName returns a human-readable frame type name for logs.
func TypeName(t byte) string {
	switch t {
	case TypeLogin:
		return "login"
	case TypeLoginResp:
		return "login-resp"
	case TypeOpen:
		return "open"
	case TypeClose:
		return "close"
	case TypeData:
		return "data"
	case TypeHeartbeat:
		return "heartbeat"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// Frame is one decoded wire frame.
type Frame struct {
	Type    byte
	ConnID  uint32
	Payload []byte
}

// ReadFrame reads exactly one frame from r. The returned payload is a fresh
// slice; ownership passes to the caller.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{
		Type:   h[0],
		ConnID: binary.BigEndian.Uint32(h[1:5]),
	}
	length := binary.BigEndian.Uint32(h[5:9])
	if length > MaxFrameSize {
		return Frame{}, fmt.Errorf("protocol: frame length %d exceeds max %d", length, MaxFrameSize)
	}
	f.Payload = make([]byte, length)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// DefaultWriteTimeout bounds every frame write on a control connection. Without
// it a peer that stops reading pins this writer's mutex forever, which stalls
// every stream sharing the connection — in the 2026-10-05 outage the client's
// LAN pump held that mutex behind one blocked write and the whole session went
// silently dead (control frames kept flowing, not a byte of data did).
const DefaultWriteTimeout = 45 * time.Second

// ConnWriter serializes writes to one control connection. Tunneled-data
// pumps and control goroutines share it, so every write takes a mutex; each
// frame is flushed before returning. After the first error the connection is
// closed and all later writes fail fast.
type ConnWriter struct {
	mu      sync.Mutex
	w       *bufio.Writer
	conn    net.Conn
	err     error
	timeout time.Duration // per-frame write deadline; 0 disables it
}

// NewConnWriter wraps conn with a buffered, mutex-guarded frame writer.
func NewConnWriter(conn net.Conn) *ConnWriter {
	return &ConnWriter{w: bufio.NewWriterSize(conn, 32<<10), conn: conn, timeout: DefaultWriteTimeout}
}

// SetWriteTimeout overrides the per-frame deadline (0 disables it).
func (cw *ConnWriter) SetWriteTimeout(d time.Duration) {
	cw.mu.Lock()
	cw.timeout = d
	cw.mu.Unlock()
}

// Write sends one frame (header + payload).
func (cw *ConnWriter) Write(typ byte, connID uint32, payload []byte) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.err != nil {
		return cw.err
	}
	if len(payload) > MaxFrameSize {
		cw.fail(fmt.Errorf("protocol: payload %d exceeds max %d", len(payload), MaxFrameSize))
		return cw.err
	}
	if cw.timeout > 0 {
		// Refreshed every frame: a stalled peer fails the write (fail() then
		// closes the conn, so every later write returns immediately) instead of
		// holding this mutex for good.
		_ = cw.conn.SetWriteDeadline(time.Now().Add(cw.timeout))
	}
	err := writeHeader(cw.w, typ, connID, len(payload))
	if err == nil && len(payload) > 0 {
		_, err = cw.w.Write(payload)
	}
	if err == nil {
		err = cw.w.Flush()
	}
	if err != nil {
		cw.fail(err)
	}
	return err
}

// WriteMsg marshals v as JSON and sends it as one frame on connID.
func (cw *ConnWriter) WriteMsg(typ byte, connID uint32, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return cw.Write(typ, connID, b)
}

func (cw *ConnWriter) fail(err error) {
	if cw.err == nil {
		cw.err = err
		_ = cw.conn.Close()
	}
}

func writeHeader(w io.Writer, typ byte, connID uint32, length int) error {
	var h [HeaderSize]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:5], connID)
	binary.BigEndian.PutUint32(h[5:9], uint32(length))
	_, err := w.Write(h[:])
	return err
}

var ErrWrongType = errors.New("protocol: unexpected frame type")
