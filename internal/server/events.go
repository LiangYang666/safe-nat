package server

import (
	"sync"
	"time"
)

// Event types pushed to web UI subscribers (SSE, design.md §7).
const (
	EvClientUp   = "client_up"   // a client session became ready
	EvClientDown = "client_down" // a client session ended
	EvConnOpen   = "conn_open"   // a public connection passed the firewall and was opened
	EvConnClose  = "conn_close"  // a public connection ended
	EvBlocked    = "blocked"     // a connection was denied by the whitelist firewall
)

// Event is one real-time notification. The Time field is RFC3339 UTC.
type Event struct {
	Type       string `json:"type"`
	Time       string `json:"time"`
	Client     string `json:"client,omitempty"`      // session label (name or addr)
	ClientIP   string `json:"client_ip,omitempty"`   // client-side address of the session
	Tunnel     string `json:"tunnel,omitempty"`      // tunnel name
	TypeTunnel string `json:"tunnel_type,omitempty"` // tcp | socks5
	RemotePort uint16 `json:"remote_port,omitempty"`
	IP         string `json:"ip,omitempty"`      // the public peer's address
	ConnID     uint32 `json:"conn_id,omitempty"` // tunneled connection id
	Reason     string `json:"reason,omitempty"`  // e.g. close reason
	Detail     string `json:"detail,omitempty"`  // human-readable extra (e.g. denied rule count)
}

func newEvent(typ string) Event {
	return Event{Type: typ, Time: time.Now().UTC().Format(time.RFC3339)}
}

// Hub fans events out to any number of subscribers. Publishing never blocks:
// a subscriber whose buffer is full is dropped rather than stalling the data
// plane.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

const subBuf = 512

// NewHub creates an empty event hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new subscriber channel. The returned cancel function
// unregisters it and must be called when the subscriber is done.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subBuf)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Publish delivers ev to every subscriber without blocking.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop it rather than stall the tunnel
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Len reports the number of active subscribers (for tests/debug).
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
