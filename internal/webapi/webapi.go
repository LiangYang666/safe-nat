// Package webapi serves the safe-nat management API and embeds the web UI
// (design.md §7). Everything under /api/* except POST /api/login requires a
// valid session cookie. The SPA shell and its assets are served from the
// embedded static/ directory.
package webapi

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/LiangYang666/safe-nat/internal/config"
	"github.com/LiangYang666/safe-nat/internal/server"
	"github.com/LiangYang666/safe-nat/internal/throttle"
)

//go:embed static
var staticFS embed.FS

// WebAPI owns the HTTP server and the auth session store.
type WebAPI struct {
	srv          *server.Server // management state + event hub
	wc           *config.WebConfig
	log          *slog.Logger
	sess         *sessionStore
	loginLimiter *throttle.Limiter // per-IP escalating lock on failed logins
}

// Run serves the management UI/API on wc.BindPort until ctx is cancelled.
// A bind failure is returned synchronously so the caller can fail fast.
func Run(ctx context.Context, srv *server.Server, wc *config.WebConfig, log *slog.Logger) error {
	a := &WebAPI{
		srv:          srv,
		wc:           wc,
		log:          log,
		sess:         newSessionStore(sessionTTL),
		loginLimiter: throttle.New(),
	}
	addr := fmt.Sprintf(":%d", wc.BindPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("webapi: listen %s: %w", addr, err)
	}
	hs := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
	}()
	log.Info("web api listening", "addr", ln.Addr().String())
	if err := hs.Serve(ln); err != nil && ctx.Err() == nil {
		return fmt.Errorf("webapi: serve: %w", err)
	}
	return nil
}

func (a *WebAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.Handle("POST /api/logout", a.requireSession(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /api/session", a.requireSession(http.HandlerFunc(a.handleSession)))
	mux.Handle("GET /api/tunnels", a.requireSession(http.HandlerFunc(a.handleTunnels)))
	mux.Handle("GET /api/sessions", a.requireSession(http.HandlerFunc(a.handleSessions)))
	mux.Handle("GET /api/stats", a.requireSession(http.HandlerFunc(a.handleStats)))
	mux.Handle("GET /api/whitelist", a.requireSession(http.HandlerFunc(a.handleWhitelistList)))
	mux.Handle("POST /api/whitelist", a.requireSession(http.HandlerFunc(a.handleWhitelistAdd)))
	mux.Handle("DELETE /api/whitelist/{id}", a.requireSession(http.HandlerFunc(a.handleWhitelistDelete)))
	mux.Handle("GET /api/events", a.requireSession(http.HandlerFunc(a.handleEvents)))

	// SPA + assets. Unknown non-API GET paths fall back to index.html so a
	// page refresh keeps working.
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("webapi: embed static: %v", err))
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			writeErr(w, http.StatusNotFound, "no such API endpoint")
			return
		}
		// Serve real files (assets); everything else goes to the SPA shell.
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, sub, "index.html")
	})
	return logMiddleware(a.log, mux)
}

func logMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/events" { // SSE streams are noisy
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur_ms", time.Since(start).Milliseconds(), "from", r.RemoteAddr)
		}
	})
}
