package protocol

import (
	"encoding/json"
	"fmt"
)

// Control messages. All are JSON-encoded in frames with ConnID == 0.

// Login is sent by the client once, immediately after the TCP connect.
type Login struct {
	Token   string   `json:"token"`
	Tunnels []Tunnel `json:"tunnels"`
}

// Tunnel describes one port mapping the client asks the server to expose.
type Tunnel struct {
	Name       string `json:"name"`
	RemotePort uint16 `json:"remote_port"`
	Firewall   bool   `json:"firewall"` // when true (default), accept is gated by whitelist
}

// LoginResp reports per-tunnel bind results back to the client.
type LoginResp struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitempty"`
	Results []TunnelResult `json:"results"`
}

// TunnelResult is the bind outcome of one requested tunnel.
type TunnelResult struct {
	Name       string `json:"name"`
	RemotePort uint16 `json:"remote_port"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// Open tells the client that a public connection arrived on remote_port;
// the frame header carries the fresh connID that both sides use afterwards.
type Open struct {
	RemotePort uint16 `json:"remote_port"`
}

// Close tells the peer to tear down the tunneled connection connID.
type Close struct {
	Reason string `json:"reason,omitempty"`
}

// DecodeJSON unmarshals a control frame payload into v.
func DecodeJSON(f Frame, v any) error {
	if f.Type == TypeData || f.Type == TypeHeartbeat {
		return fmt.Errorf("protocol: frame type %s carries no control message", TypeName(f.Type))
	}
	return json.Unmarshal(f.Payload, v)
}
