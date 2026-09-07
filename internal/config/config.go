// Package config loads and validates safe-nat YAML configs.
// Keep the format flat and close to the LiangNat configs users already know.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/LiangYang666/safe-nat/internal/tlsx"
)

// ---------- server ----------

// ServerConfig mirrors config_server.yaml.
type ServerConfig struct {
	BindPort int        `yaml:"bind_port"`
	Token    string     `yaml:"token"`
	Web      *WebConfig `yaml:"web"` // nil => web management + whitelist disabled

	// TLS (auto transport encryption, v0.7). Enabled by default: the server
	// generates a self-signed keypair on first start (tls_cert/tls_key,
	// default <config dir>/data/safenat-server.crt|.key — data stays out of
	// the config directory) and the client pins its fingerprint. Set
	// tls: false to disable.
	TLS     *bool  `yaml:"tls"`
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
}

// WebConfig enables the built-in management UI / whitelist firewall.
type WebConfig struct {
	BindPort int    `yaml:"bind_port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DBPath   string `yaml:"db_path"` // sqlite file for the whitelist; default data/safenat.db
}

// ---------- client ----------

// ClientConfig mirrors config_client.yaml.
type ClientConfig struct {
	Name       string                  `yaml:"name"` // optional label shown in the web UI
	ServerAddr string                  `yaml:"server_addr"`
	ServerPort int                     `yaml:"server_port"`
	Token      string                  `yaml:"token"`
	Tunnels    map[string]TunnelConfig `yaml:"tunnels"`
	Socks5     *Socks5Config           `yaml:"socks5"` // optional built-in SOCKS5 proxy (M3)

	// TLS (auto transport encryption, v0.7). Enabled by default; the server's
	// certificate fingerprint is trusted on first contact and stored in
	// tls_fingerprints (default <config dir>/known_servers.txt). Set tls:
	// false to disable.
	TLS             *bool  `yaml:"tls"`
	TLSFingerprints string `yaml:"tls_fingerprints"`
}

// Socks5Config enables a SOCKS5 proxy service: the server binds
// remote_port and parses CONNECT requests; the client dials the targets.
type Socks5Config struct {
	RemotePort int   `yaml:"remote_port"`
	Firewall   *bool `yaml:"firewall"` // default true: whitelist-gate proxy users
}

// FirewallEnabled resolves the *bool default.
func (s *Socks5Config) FirewallEnabled() bool {
	return s.Firewall == nil || *s.Firewall
}

// TunnelConfig is one port mapping.
type TunnelConfig struct {
	Type       string `yaml:"type"` // "tcp" (default); socks5 planned M3
	LocalIP    string `yaml:"local_ip"`
	LocalPort  int    `yaml:"local_port"`
	RemotePort int    `yaml:"remote_port"`
	// Firewall defaults to true when unset: the remote port is protected by
	// the whitelist firewall once web management is enabled on the server.
	Firewall *bool `yaml:"firewall"`
}

// FirewallEnabled resolves the *bool default.
func (t *TunnelConfig) FirewallEnabled() bool {
	return t.Firewall == nil || *t.Firewall
}

// ---------- loaders ----------

func loadFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // catch typos
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}

// dataDir returns the default state directory for a config file: a data/
// sibling next to the config, keeping mutable state (certificates, the
// fingerprint store, sqlite) out of the config directory.
func dataDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "data")
}

// LoadServer reads and validates config_server.yaml.
func LoadServer(path string) (*ServerConfig, []string, error) {
	var cfg ServerConfig
	if err := loadFile(path, &cfg); err != nil {
		return nil, nil, err
	}
	var warns []string
	if cfg.BindPort == 0 {
		cfg.BindPort = 10010
		warns = append(warns, "bind_port unset, using 10010")
	}
	if cfg.Token == "" {
		cfg.Token = "123456"
		warns = append(warns, "token unset, using default \"123456\" — change it before exposing publicly")
	}
	if err := checkPort("bind_port", cfg.BindPort); err != nil {
		return nil, warns, err
	}
	if cfg.Web != nil {
		if cfg.Web.BindPort == 0 {
			cfg.Web.BindPort = 10086
			warns = append(warns, "web.bind_port unset, using 10086")
		}
		if err := checkPort("web.bind_port", cfg.Web.BindPort); err != nil {
			return nil, warns, err
		}
		if cfg.Web.BindPort == cfg.BindPort {
			return nil, warns, fmt.Errorf("config: web.bind_port must differ from bind_port")
		}
		if cfg.Web.Username == "" {
			cfg.Web.Username = "admin"
			warns = append(warns, "web.username unset, using \"admin\"")
		}
		if cfg.Web.Password == "" {
			cfg.Web.Password = "123456"
			warns = append(warns, "web.password unset, using default — change it")
		}
		if cfg.Web.DBPath == "" {
			cfg.Web.DBPath = filepath.Join(dataDir(path), "safenat.db")
			warns = append(warns, "web.db_path unset, using \""+cfg.Web.DBPath+"\"")
		}
	}
	if tlsx.Enabled(cfg.TLS) {
		dir := dataDir(path)
		if cfg.TLSCert == "" {
			cfg.TLSCert = filepath.Join(dir, "safenat-server.crt")
			warns = append(warns, "tls on: cert auto-generated at "+cfg.TLSCert)
		}
		if cfg.TLSKey == "" {
			cfg.TLSKey = filepath.Join(dir, "safenat-server.key")
		}
	}
	return &cfg, warns, nil
}

// LoadClient reads and validates config_client.yaml.
func LoadClient(path string) (*ClientConfig, []string, error) {
	var cfg ClientConfig
	if err := loadFile(path, &cfg); err != nil {
		return nil, nil, err
	}
	var warns []string
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.ServerAddr == "" {
		return nil, warns, fmt.Errorf("config: server_addr is required")
	}
	if cfg.ServerPort == 0 {
		cfg.ServerPort = 10010
		warns = append(warns, "server_port unset, using 10010")
	}
	if cfg.Token == "" {
		return nil, warns, fmt.Errorf("config: token is required (must match server)")
	}
	if err := checkPort("server_port", cfg.ServerPort); err != nil {
		return nil, warns, err
	}
	if tlsx.Enabled(cfg.TLS) && cfg.TLSFingerprints == "" {
		cfg.TLSFingerprints = filepath.Join(dataDir(path), "known_servers.txt")
		warns = append(warns, "tls on: client pins server fingerprint at "+cfg.TLSFingerprints)
	}
	if len(cfg.Tunnels) == 0 {
		return nil, warns, fmt.Errorf("config: at least one tunnel required")
	}
	if len(cfg.Tunnels) > 100 {
		return nil, warns, fmt.Errorf("config: too many tunnels (%d, max 100)", len(cfg.Tunnels))
	}
	seen := map[int]string{}
	for name, t := range cfg.Tunnels {
		if name == "" {
			return nil, warns, fmt.Errorf("config: tunnel name must not be empty")
		}
		if t.Type == "" {
			t.Type = "tcp"
		}
		if t.Type != "tcp" {
			return nil, warns, fmt.Errorf("config: tunnel %q: unsupported type %q (tunnels are tcp; socks5 is a top-level section)", name, t.Type)
		}
		if t.LocalIP == "" {
			t.LocalIP = "127.0.0.1"
		}
		if err := checkPort("tunnels."+name+".local_port", t.LocalPort); err != nil {
			return nil, warns, err
		}
		if err := checkPort("tunnels."+name+".remote_port", t.RemotePort); err != nil {
			return nil, warns, err
		}
		if prev, dup := seen[t.RemotePort]; dup {
			return nil, warns, fmt.Errorf("config: tunnels %q and %q both request remote_port %d", prev, name, t.RemotePort)
		}
		seen[t.RemotePort] = name
		cfg.Tunnels[name] = t
	}
	if cfg.Socks5 != nil {
		if err := checkPort("socks5.remote_port", cfg.Socks5.RemotePort); err != nil {
			return nil, warns, err
		}
		for name, t := range cfg.Tunnels {
			if t.RemotePort == cfg.Socks5.RemotePort {
				return nil, warns, fmt.Errorf("config: socks5.remote_port %d collides with tunnel %q", cfg.Socks5.RemotePort, name)
			}
		}
	}
	return &cfg, warns, nil
}

func checkPort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("config: %s must be 1-65535, got %d", field, port)
	}
	return nil
}
