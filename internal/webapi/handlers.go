package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LiangYang666/safe-nat/internal/blocklog"
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

// handleWhitelistMe reports the caller's own address, its region and whether
// it is already covered by a whitelist rule (exact or CIDR), so the UI can
// show a green "already allowed" / red "not allowed, add me" card.
func (a *WebAPI) handleWhitelistMe(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	rule, covered := a.srv.WhiteMatch(ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"ip":      ip,
		"region":  geodb.Region(ip),
		"covered": covered,
		"rule":    rule, // the stored rule that covers ip, "" if none
	})
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

// handleTrafficSeries returns a time series for the curve chart. Query:
// days (1..7, default 1), bucket (m|h, default m — the UI asks for hour
// buckets on 7-day views), tunnel (optional filter).
func (a *WebAPI) handleTrafficSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 1
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 7 {
			writeErr(w, http.StatusBadRequest, "days must be 1..7")
			return
		}
		days = n
	}
	bucket := "m"
	if v := q.Get("bucket"); v == "h" {
		bucket = "h"
	}
	rows, err := a.srv.TrafficSeries(q.Get("tunnel"), days, bucket)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []traffic.SeriesRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// ---------- refusal log (who is being blocked) ----------

// handleBlocked returns the aggregated refusal log: one row per
// (ip, tunnel, port, kind) plus the newest raw hits. Rows are enriched with
// the IP's region and whether a whitelist rule already covers it — that is
// what lets the UI show "已在白名单" instead of offering a pointless
// "add me" button.
func (a *WebAPI) handleBlocked(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeErr(w, http.StatusBadRequest, "limit must be 1..1000")
			return
		}
		limit = n
	}
	recentLimit := 50
	if v := q.Get("recent"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, http.StatusBadRequest, "recent must be 1..500")
			return
		}
		recentLimit = n
	}

	type row struct {
		blocklog.Entry
		Region  string `json:"region"`  // ip2region label, "" when unknown
		Covered bool   `json:"covered"` // a whitelist rule covers this IP
		Rule    string `json:"rule,omitempty"`
	}
	out := make([]row, 0, limit)
	sum := blocklog.Summary{}
	recent := []blocklog.RecentHit{}

	if bl := a.srv.BlockedLog(); bl != nil {
		rows, err := bl.Rows(limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, e := range rows {
			rule, covered := a.srv.WhiteMatch(e.IP)
			out = append(out, row{Entry: e, Region: geodb.Region(e.IP), Covered: covered, Rule: rule})
		}
		if sum, err = bl.Summary(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if recent, err = bl.Recent(recentLimit); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": sum,
		"rows":    out,
		"recent":  recent,
	})
}

// handleBlockedClear wipes the refusal log (UI "清空记录").
func (a *WebAPI) handleBlockedClear(w http.ResponseWriter, r *http.Request) {
	bl := a.srv.BlockedLog()
	if bl == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := bl.Clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.log.Info("refusal log cleared", "by", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
