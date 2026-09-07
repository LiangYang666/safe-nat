// Command safenat is the CLI entrypoint of safe-nat:
// a NAT penetration tool whose exposed ports can be guarded by an
// IP-whitelist firewall, managed from a built-in web UI.
//
// Design: tasks/20260906-go-liangnat/design.md (in nat-dev-workspace).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/LiangYang666/safe-nat/internal/client"
	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/server"
	"github.com/LiangYang666/safe-nat/internal/tlsx"
	"github.com/LiangYang666/safe-nat/internal/webapi"
)

const version = "0.7.0" // auto TLS (TOFU) + hardening + ops polish

const usageText = `safenat - secure NAT penetration (Go)

Usage:
  safenat server [-c <server.yaml>]   run the public server (cloud side)
  safenat client [-c <client.yaml>]   run the client (LAN side)
  safenat init [server|client]        write a starter config to your user
                                      config dir (~/.config/safenat on
                                      Linux, ~/Library/Application Support
                                      /safenat on macOS) and print its path
  safenat version                     print version

Config discovery (when -c is omitted):
  server/client look for <user config dir>/safenat/{server,client}.yaml
  first, then ./config_{server,client}.yaml in the working directory.

Config:
  server: bind_port (default 10010), token, optional web: section
          (bind_port default 10086 / username / password / db_path)
          enables the management UI + IP-whitelist firewall
  client: name (optional label), server_addr, server_port, token, tunnels:
            <name>: { local_ip, local_port, remote_port, firewall }

Transport security (v0.7, on by default):
  tls: true  — the server auto-generates a self-signed keypair next to its
               config (safenat-server.crt/.key); the client pins the server
               fingerprint on first contact (known_servers.txt next to its
               config) and rejects later changes. Set tls: false to disable.

Design doc: tasks/20260907-safenat-v070/design.md
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "server":
		return runServer(args[1:])
	case "client":
		return runClient(args[1:])
	case "init":
		return runInit(args[1:])
	case "status":
		return runStatus(args[1:])
	case "logs":
		return runLogs(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("safenat %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

func newLogger(debug bool, extra io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	w := io.Writer(os.Stderr)
	if extra != nil {
		w = io.MultiWriter(os.Stderr, extra) // stderr stays primary; ring keeps history
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func parseFlags(name string, args []string) (cfgPath string, debug bool, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := fs.String("c", "", "config file path (default: discover, see `safenat help`)")
	fs.BoolVar(&debug, "debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return "", false, false
	}
	return *cfg, debug, true
}

// userConfigDir returns <UserConfigDir>/safenat — the platform convention
// for per-user app config: ~/.config/safenat (Linux), ~/Library/Application
// Support/safenat (macOS), %AppData%\safenat (Windows).
func userConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(base, "safenat"), nil
}

// resolveConfigPath implements config discovery for server/client when -c
// is omitted: user config dir first, then the legacy working-directory
// default.
func resolveConfigPath(kind string, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	candidates := []string{}
	if dir, err := userConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, kind+".yaml"))
	}
	candidates = append(candidates, "config_"+kind+".yaml")
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no config found (looked at %s); run `safenat init %s` to create one", strings.Join(candidates, ", "), kind)
}

// signalCtx returns a context cancelled by SIGINT/SIGTERM.
func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runServer(args []string) int {
	path, debug, ok := parseFlags("server", args)
	if !ok {
		return 2
	}
	path, err := resolveConfigPath("server", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ring := newRing(2000)
	log := newLogger(debug, ring).With("cmd", "server")
	cfg, warns, err := config.LoadServer(path)
	if err != nil {
		log.Error("config error", "err", err)
		return 1
	}
	for _, w := range warns {
		log.Warn(w)
	}

	ctx, stop := signalCtx()
	defer stop()

	srv, err := server.New(cfg, log)
	if err != nil {
		log.Error("server init failed", "err", err)
		return 1
	}
	defer srv.Close()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	startAdmin(ctx, "server", ring, func() any { return serverStatusData(srv, cfg) })

	errCh := make(chan error, 2)
	go func() { errCh <- srv.Run(cctx) }()
	if cfg.Web != nil {
		go func() { errCh <- webapi.Run(cctx, srv, cfg.Web, log) }()
	}
	if err := <-errCh; err != nil {
		cancel()
		log.Error("server exited", "err", err)
		return 1
	}
	cancel()
	return 0
}

func runClient(args []string) int {
	path, debug, ok := parseFlags("client", args)
	if !ok {
		return 2
	}
	path, err := resolveConfigPath("client", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ring := newRing(2000)
	log := newLogger(debug, ring).With("cmd", "client")
	cfg, warns, err := config.LoadClient(path)
	if err != nil {
		log.Error("config error", "err", err)
		return 1
	}
	for _, w := range warns {
		log.Warn(w)
	}
	ctx, stop := signalCtx()
	defer stop()
	c := client.New(cfg, log)
	startAdmin(ctx, "client", ring, func() any { return c.State() })
	if err := c.Run(ctx); err != nil {
		log.Error("client exited", "err", err)
		return 1
	}
	return 0
}

// serverStatusData assembles the payload for `safenat status server`.
func serverStatusData(srv *server.Server, cfg *config.ServerConfig) map[string]any {
	stats := srv.Stats()
	tunnels := srv.Tunnels()
	listening := []int{cfg.BindPort}
	webPort := 0
	if cfg.Web != nil {
		webPort = cfg.Web.BindPort
	}
	for _, t := range tunnels {
		listening = append(listening, int(t.RemotePort))
	}
	if webPort != 0 {
		listening = append(listening, webPort)
	}
	sort.Ints(listening)
	return map[string]any{
		"uptime_sec":      stats.UptimeSec,
		"clients":         stats.Clients,
		"tunnels":         stats.Tunnels,
		"conn_active":     stats.ConnActive,
		"conn_total":      stats.ConnTotal,
		"blocked_total":   stats.BlockedTotal,
		"whitelist_rules": stats.Whitelist,
		"tls":             tlsx.Enabled(cfg.TLS),
		"listening_ports": listening,
		"web_ui":          map[string]any{"enabled": cfg.Web != nil, "port": webPort},
		"sessions":        srv.Sessions(),
		"tunnel_views":    tunnels,
	}
}

// runInit writes a starter config into the user config dir (never
// overwriting) and prints its path — the modern-CLI onboarding path.
func runInit(args []string) int {
	if len(args) != 1 || (args[0] != "server" && args[0] != "client") {
		fmt.Fprintf(os.Stderr, "usage: safenat init [server|client]\n")
		return 2
	}
	kind := args[0]
	dir, err := userConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "init: mkdir %s: %v\n", dir, err)
		return 1
	}
	target := filepath.Join(dir, kind+".yaml")
	if _, err := os.Stat(target); err == nil {
		fmt.Fprintf(os.Stderr, "init: %s already exists, not overwriting\n", target)
		return 1
	}
	tmpl := serverInitConfig
	if kind == "client" {
		tmpl = clientInitConfig
	}
	if err := os.WriteFile(target, []byte(tmpl), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "init: write %s: %v\n", target, err)
		return 1
	}
	fmt.Printf("created %s\n\nEdit it, then run:\n  safenat %s\n", target, kind)
	return 0
}

const serverInitConfig = `# safe-nat server config (cloud side). Written by "safenat init".
# Layout convention: this file is your CONFIG (edit freely); certificates,
# the whitelist database and other mutable state live in a data/ directory
# next to this file by default (override with tls_cert/tls_key/db_path).
# Transport is encrypted by default: the first start auto-generates
# data/safenat-server.crt/.key. Keep them — deleting them changes the
# fingerprint and every client will refuse to connect until you delete its
# data/known_servers.txt.

bind_port: 10010   # control port; the client connects here
token: "change-me-please-use-a-long-random-string"

# Enables the web management UI (login, whitelist CRUD, live tunnel state)
# and the IP-whitelist firewall. Remove the block to disable both.
# db_path defaults to data/safenat.db (next to this config).
web:
  bind_port: 10086
  username: admin
  password: "change-me-too"
`

const clientInitConfig = `# safe-nat client config (LAN side). Written by "safenat init".
# TLS is on by default: on first connect the client pins the server's
# certificate fingerprint to data/known_servers.txt (next to this file) and
# rejects it if the server ever presents a different one. If the server is
# legitimately reinstalled, delete that file and reconnect.

name: "my-lan"          # optional label shown in the server web UI
server_addr: 127.0.0.1  # public server address
server_port: 10010
token: "change-me"      # must match the server config

tunnels:
  ssh:
    local_ip: 127.0.0.1   # the LAN machine/port to expose
    local_port: 22
    remote_port: 40022    # public port on the server
    # firewall: true      # (default) whitelist-gate this port once web is on

# Optional SOCKS5 proxy: the server binds remote_port and this client dials
# targets from its LAN (e.g. browse as if you were on this network).
# socks5:
#   remote_port: 7999
#   firewall: true
`
