// Package server implements the safe-nat public server: it accepts control
// connections from clients, binds their requested remote ports, enforces the
// whitelist firewall at accept time and pumps tunneled data over the single
// control connection (design.md §3).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/LiangYang666/safe-nat/internal/config"
)

// Server owns the control listener and accepts client sessions.
type Server struct {
	cfg *config.ServerConfig
	log *slog.Logger
}

// Run listens on cfg.BindPort until ctx is cancelled.
func Run(ctx context.Context, cfg *config.ServerConfig, log *slog.Logger) error {
	s := &Server{cfg: cfg, log: log}
	addr := fmt.Sprintf(":%d", cfg.BindPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", addr, err)
	}
	log.Info("server listening", "addr", ln.Addr().String(), "web", cfg.Web != nil)

	var sessions sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // shutting down
			}
			return fmt.Errorf("server: accept: %w", err)
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			newSession(s, conn).run()
		}()
	}
	sessions.Wait()
	log.Info("server stopped")
	return nil
}
