package webapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/LiangYang666/safe-nat/internal/server"
)

const (
	cookieName = "safenat_session"
	sessionTTL = 12 * time.Hour
)

// ---------- session store ----------

type webSession struct {
	user string
	exp  time.Time
}

type sessionStore struct {
	mu  sync.Mutex
	m   map[string]webSession
	ttl time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	ss := &sessionStore{m: make(map[string]webSession), ttl: ttl}
	go ss.cleanupLoop()
	return ss
}

func (ss *sessionStore) create(user string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	ss.mu.Lock()
	ss.m[token] = webSession{user: user, exp: time.Now().Add(ss.ttl)}
	ss.mu.Unlock()
	return token, nil
}

// get returns the session's user, sliding the expiry on every hit.
func (ss *sessionStore) get(token string) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ws, ok := ss.m[token]
	if !ok {
		return "", false
	}
	if time.Now().After(ws.exp) {
		delete(ss.m, token)
		return "", false
	}
	ws.exp = time.Now().Add(ss.ttl)
	ss.m[token] = ws
	return ws.user, true
}

func (ss *sessionStore) delete(token string) {
	ss.mu.Lock()
	delete(ss.m, token)
	ss.mu.Unlock()
}

func (ss *sessionStore) cleanupLoop() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		ss.mu.Lock()
		for tok, ws := range ss.m {
			if now.After(ws.exp) {
				delete(ss.m, tok)
			}
		}
		ss.mu.Unlock()
	}
}

// ---------- login helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireSession guards every /api handler except login.
func (a *WebAPI) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if _, ok := a.sess.get(c.Value); !ok {
			writeErr(w, http.StatusUnauthorized, "session expired")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *WebAPI) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := a.loginLimiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		a.publishLoginFail(ip, fmt.Sprintf("rate limited (%s)", wait.Round(time.Second)))
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, locked for "+wait.Round(time.Second).String())
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(a.wc.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.wc.Password)) == 1
	if !userOK || !passOK {
		a.loginLimiter.Fail(ip)
		a.publishLoginFail(ip, "invalid credentials")
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	a.loginLimiter.Reset(ip)
	token, err := a.sess.create(a.wc.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "session error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": a.wc.Username, "version": a.version})
}

// publishLoginFail surfaces a failed web login to the live security log
// (visible to currently logged-in admins over SSE).
func (a *WebAPI) publishLoginFail(ip, reason string) {
	a.log.Warn("web login rejected", "ip", ip, "reason", reason)
	a.srv.Publish(server.Event{Type: server.EvLoginFail, Time: time.Now().UTC().Format(time.RFC3339), ClientIP: ip, Reason: reason})
}

func (a *WebAPI) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.sess.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *WebAPI) handleSession(w http.ResponseWriter, r *http.Request) {
	// reachable only with a valid session; report who we are
	c, _ := r.Cookie(cookieName)
	user, _ := a.sess.get(c.Value)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": user, "version": a.version})
}
