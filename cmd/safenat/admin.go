package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// adminServer exposes a local, unauthenticated control endpoint over a
// unix socket (mode 0600, owner-only) — the local-file permission is the
// ACL, so no password is needed (unlike the public web API). Endpoints:
//
//	GET /status   process + tunnel state (JSON)
//	GET /logs?n=N last N log lines (plain text)
type adminServer struct {
	kind    string
	logs    *ringBuf
	dataFn  func() any
	httpSrv *http.Server
	path    string
}

// startAdmin listens on the unix socket until ctx is cancelled. On failure
// (e.g. socket already in use) it logs and continues — the admin channel is
// best-effort, never a reason to take the tunnel down.
func startAdmin(ctx context.Context, kind string, logs *ringBuf, dataFn func() any) {
	path := sockPath(kind)
	_ = os.Remove(path) // stale socket from a crashed process
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin: listen %s: %v (status/logs unavailable)\n", path, err)
		return
	}
	_ = os.Chmod(path, 0o600)
	a := &adminServer{kind: kind, logs: logs, dataFn: dataFn, path: path}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", a.handleStatus)
	mux.HandleFunc("/logs", a.handleLogs)
	a.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = a.httpSrv.Shutdown(shutCtx)
		_ = ln.Close()
		_ = os.Remove(path)
	}()
	go func() {
		if err := a.httpSrv.Serve(ln); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "admin: serve: %v\n", err)
		}
	}()
}

func (a *adminServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeAdminJSON(w, map[string]any{
		"kind":    a.kind,
		"version": version,
		"data":    a.dataFn(),
	})
}

func (a *adminServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	for _, line := range a.logs.Tail(n) {
		fmt.Fprintln(w, line)
	}
}

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------- client side: `safenat status` / `safenat logs` ----------

// adminGet performs an HTTP GET against the unix-socket admin endpoint.
func adminGet(kind, path string) ([]byte, error) {
	sock := sockPath(kind)
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("no local %s process found (%s): is it running as this user?", kind, sock)
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	resp, err := client.Get("http://unix" + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("admin: %s: %s", path, string(body))
	}
	return body, nil
}

func runStatus(args []string) int {
	kind := "server"
	if len(args) >= 1 {
		kind = args[0]
	}
	if kind != "server" && kind != "client" {
		fmt.Fprintln(os.Stderr, "usage: safenat status [server|client]")
		return 2
	}
	body, err := adminGet(kind, "/status")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var st struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
		Data    any    `json:"data"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		fmt.Fprintln(os.Stderr, "admin: bad status payload:", err)
		return 1
	}
	pretty, err := json.MarshalIndent(st.Data, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%s %s (local)\n%s\n", st.Kind, st.Version, pretty)
	return 0
}

func runLogs(args []string) int {
	kind := "server"
	n := 200
	if len(args) >= 1 && (args[0] == "server" || args[0] == "client") {
		kind = args[0]
		args = args[1:]
	}
	if len(args) == 1 {
		if parsed, err := strconv.Atoi(args[0]); err == nil && parsed > 0 {
			n = parsed
		}
	}
	body, err := adminGet(kind, "/logs?n="+strconv.Itoa(n))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(string(body))
	return 0
}
