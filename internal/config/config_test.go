package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServerDefaults(t *testing.T) {
	// Empty server file: bind_port defaults to 10010; a bare web: block
	// defaults to port 10086.
	cfg, warns, err := LoadServer(writeTemp(t, "token: secret\nweb:\n  password: p\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindPort != 10010 {
		t.Errorf("bind_port default = %d, want 10010", cfg.BindPort)
	}
	if cfg.Web == nil || cfg.Web.BindPort != 10086 {
		t.Errorf("web.bind_port default = %+v, want 10086", cfg.Web)
	}
	found := map[string]bool{}
	for _, w := range warns {
		found[w] = true
	}
	if !found["bind_port unset, using 10010"] || !found["web.bind_port unset, using 10086"] {
		t.Errorf("expected default-port warnings, got %v", warns)
	}
}

func TestClientDefaultServerPort(t *testing.T) {
	cfg, _, err := LoadClient(writeTemp(t, "server_addr: 1.2.3.4\ntoken: t\ntunnels:\n  a:\n    local_port: 22\n    remote_port: 40022\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerPort != 10010 {
		t.Errorf("server_port default = %d, want 10010", cfg.ServerPort)
	}
}

func TestExplicitPortsWin(t *testing.T) {
	cfg, warns, err := LoadServer(writeTemp(t, "bind_port: 20010\ntoken: s\nweb:\n  bind_port: 20086\n  password: p\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindPort != 20010 || cfg.Web.BindPort != 20086 {
		t.Errorf("explicit ports not honored: %d / %d", cfg.BindPort, cfg.Web.BindPort)
	}
	for _, w := range warns {
		if w == "bind_port unset, using 10010" || w == "web.bind_port unset, using 10086" {
			t.Errorf("unexpected default warning %q", w)
		}
	}
}
