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
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/LiangYang666/safe-nat/internal/client"
	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/server"
)

const version = "0.2.0" // M1: minimal tunnel loop

const usageText = `safenat - secure NAT penetration (Go)

Usage:
  safenat server -c <server.yaml>   run the public server (cloud side)
  safenat client -c <client.yaml>   run the client (LAN side)
  safenat version                   print version

Config:
  server: bind_port (default 10101), token, optional web: section (M2)
  client: server_addr, server_port, token, tunnels:
            <name>: { local_ip, local_port, remote_port, firewall }

Design doc: tasks/20260906-go-liangnat/design.md
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

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func parseFlags(name string, args []string) (cfgPath string, debug bool, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := fs.String("c", "config_"+name+".yaml", "config file path")
	fs.BoolVar(&debug, "debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return "", false, false
	}
	return *cfg, debug, true
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
	log := newLogger(debug).With("cmd", "server")
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
	if err := server.Run(ctx, cfg, log); err != nil {
		log.Error("server exited", "err", err)
		return 1
	}
	return 0
}

func runClient(args []string) int {
	path, debug, ok := parseFlags("client", args)
	if !ok {
		return 2
	}
	log := newLogger(debug).With("cmd", "client")
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
	if err := c.Run(ctx); err != nil {
		log.Error("client exited", "err", err)
		return 1
	}
	return 0
}
