// Package relay holds the per-stream relay policy shared by both ends of a
// safe-nat tunnel: how much a single stream may buffer, how long one write to a
// peer may take, and how long a session's read loop may wait for a stalled
// stream to make progress.
//
// Why this package exists (2026-10-05 outage): both ends used to relay stream
// data *inline* inside their single session read loop, and neither write had a
// deadline. One peer that stopped reading — a visitor who walked away, a LAN
// service that stalled — therefore blocked that loop forever, which froze every
// other tunnel carried by the same client session: process alive, systemd
// "active", panel still logging conn opens, and zero bytes moving. Relaying
// through a per-stream writer goroutine behind a bounded queue keeps the read
// loop moving; the two timeouts below make sure a stream that truly stopped can
// only ever cost its own slot, never the session.
package relay

import (
	"net"
	"time"
)

var (
	// QueueLen is how many chunks one stream may buffer before its producer has
	// to wait (and, if the wait proves pointless, before that stream is reaped).
	// 64 * 32KiB = 2MiB per stalled stream.
	QueueLen = 64

	// WriteTimeout bounds a single write to a peer. A peer that cannot absorb one
	// chunk within this is treated as gone; without it a stuck write would pin a
	// goroutine (and, on the shared control connection, the writer mutex) forever.
	WriteTimeout = 20 * time.Second

	// StallTimeout bounds how long a read loop waits for a full queue to free up.
	// A slow-but-alive peer frees a slot as soon as its writer takes one chunk
	// (milliseconds to tens of milliseconds), so this only expires for a peer that
	// made no progress at all — the caller then reaps that one stream instead of
	// holding the session hostage.
	//
	// Trade-off (measured on loopback, 2026-10-05): while a stalled stream sits on
	// a full queue, the session's read loop parks for at most this long, so other
	// tunnels on the same client can hiccup by up to StallTimeout. Shorter
	// tolerates less backlog from a genuinely slow consumer (the queue only fills
	// when it is 2MiB behind); longer costs other streams more than it is worth.
	// 5s => a peer must sustain under ~6KiB/s with a full queue to be reaped.
	StallTimeout = 5 * time.Second
)

// Enqueue hands one chunk to a stream's writer goroutine. It never blocks the
// caller longer than StallTimeout, so the shared read loop cannot be parked by
// one stalled stream. Returns false when the stream made no progress (the
// caller should tear it down).
func Enqueue(out chan<- []byte, quit <-chan struct{}, chunk []byte) bool {
	select {
	case out <- chunk:
		return true
	case <-quit:
		return true // stream already gone: drop silently
	default:
	}
	t := time.NewTimer(StallTimeout)
	defer t.Stop()
	select {
	case out <- chunk:
		return true
	case <-quit:
		return true
	case <-t.C:
		return false
	}
}

// WriteAll writes the whole chunk to c under WriteTimeout.
func WriteAll(c net.Conn, chunk []byte) error {
	if err := c.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	_, err := c.Write(chunk)
	return err
}
