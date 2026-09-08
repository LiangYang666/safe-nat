package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LiangYang666/safe-nat/internal/geodb"
	"github.com/LiangYang666/safe-nat/internal/traffic"
	"github.com/LiangYang666/safe-nat/internal/whitelist"
)

// ---------- tunnels / sessions / stats ----------

func (a *WebAPI) handleTunnels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.srv.Tunnels())
}

func (a *WebAPI) handleSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.srv.Sessions())
}

func (a *WebAPI) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.srv.Stats())
}

// ---------- whitelist CRUD ----------

func (a *WebAPI) store() *whitelist.Store {
	return a.srv.Whitelist()
}

func (a *WebAPI) handleWhitelistList(w http.ResponseWriter, r *http.Request) {
	rules, err := a.store().List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []whitelist.Rule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (a *WebAPI) handleWhitelistAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule string `json:"rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	rule, err := a.store().Add(req.Rule, regionOf(req.Rule))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.log.Info("whitelist rule added", "rule", rule.Rule, "region", rule.Region, "by", clientIP(r))
	writeJSON(w, http.StatusOK, rule)
}

// regionOf resolves a rule string to a human region label. Only exact IPv4
// rules carry a region; CIDR blocks and unresolvable addresses get "".
func regionOf(rule string) string {
	if !strings.Contains(rule, "/") {
		if r := geodb.Region(rule); r != "" {
			return r
		}
	}
	return ""
}

// handleWhitelistMe reports the caller's own address and its region, so the
// UI can offer a one-click "add my current IP" (LiangNat-style).
func (a *WebAPI) handleWhitelistMe(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	writeJSON(w, http.StatusOK, map[string]string{"ip": ip, "region": geodb.Region(ip)})
}

func (a *WebAPI) handleWhitelistDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ok, err := a.store().Delete(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	a.log.Info("whitelist rule deleted", "id", id, "by", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTrafficLive streams the real-time per-tunnel rates + totals.
func (a *WebAPI) handleTrafficLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.srv.TrafficLive())
}

// handleTrafficDaily lists per-day per-tunnel totals. Query params:
// days (default 7), tunnel (optional filter).
func (a *WebAPI) handleTrafficDaily(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			writeErr(w, http.StatusBadRequest, "days must be 1..90")
			return
		}
		days = n
	}
	tunnel := r.URL.Query().Get("tunnel")
	rows, err := a.srv.TrafficDaily(tunnel, days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []traffic.DailyRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// ---------- SSE event stream ----------

// handleEvents streams live server events to the UI (design.md §7).
func (a *WebAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch, cancel := a.srv.SubscribeEvents()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			fl.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n") // comment line keeps the stream alive
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
