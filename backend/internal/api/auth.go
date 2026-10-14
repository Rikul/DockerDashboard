package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "dd_session"
	csrfHeader    = "X-Requested-With"
	csrfValue     = "dockerdashboard"
	sessionTTL    = 7 * 24 * time.Hour
)

// sessionValue derives the cookie value from the token, so changing the
// token signs everyone out and the token itself never sits in a cookie.
func (s *Server) sessionValue() string {
	m := hmac.New(sha256.New, []byte(s.cfg.Token))
	m.Write([]byte("dockerdashboard-session-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

// Guessing the token is slowed down per client address: after repeated
// failures from one address, each further attempt from it waits before it is
// answered, and attempts from one address are checked one at a time so
// sending many in parallel does not get around the wait. Other clients, and
// successful requests elsewhere, neither reset nor inherit that wait.
var failStep = 500 * time.Millisecond // a var so tests can shorten it

const (
	failWindow   = 15 * time.Minute
	maxFailSteps = 10
	maxTracked   = 4096 // addresses remembered before old entries are pruned
)

type attempts struct {
	mu       sync.Mutex // held for the whole check, serializing one address
	failures int
	lastFail time.Time
}

func (s *Server) attemptsFor(addr string) *attempts {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginAttempts == nil {
		s.loginAttempts = map[string]*attempts{}
	}
	if a := s.loginAttempts[addr]; a != nil {
		return a
	}
	if len(s.loginAttempts) >= maxTracked && time.Since(s.loginPruned) > time.Minute {
		s.loginPruned = time.Now()
		for k, v := range s.loginAttempts {
			if v.mu.TryLock() {
				stale := time.Since(v.lastFail) > failWindow
				v.mu.Unlock()
				if stale {
					delete(s.loginAttempts, k)
				}
			}
		}
	}
	if len(s.loginAttempts) >= maxTracked {
		// Still full of recent failures: new addresses share one bucket
		// rather than growing the map, so they are throttled together.
		addr = overflowAddr
		if a := s.loginAttempts[addr]; a != nil {
			return a
		}
	}
	a := &attempts{}
	s.loginAttempts[addr] = a
	return a
}

// overflowAddr is the shared bucket used once maxTracked addresses are
// tracked. It is not a valid address, so no real client maps to it.
const overflowAddr = "overflow"

// clientAddr is the address failures are counted against. It is the TCP
// peer: X-Forwarded-For is not trusted, since any client can set it. IPv6
// peers are grouped by /64, since one host usually controls a whole /64.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() && !ip.Is4In6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return host
}

// checkToken compares a candidate token against the configured one. Every
// path that accepts the raw token (login and bearer auth) goes through here.
func (s *Server) checkToken(r *http.Request, candidate string) bool {
	a := s.attemptsFor(clientAddr(r))
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.lastFail) > failWindow {
		a.failures = 0
	}
	if a.failures > 0 {
		t := time.NewTimer(time.Duration(min(a.failures, maxFailSteps)) * failStep)
		select {
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return false
		}
	}
	if equal(candidate, s.cfg.Token) {
		a.failures = 0
		return true
	}
	a.failures++
	a.lastFail = time.Now()
	return false
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// authState reports whether r is signed in, and how.
func (s *Server) authState(r *http.Request) (ok, bearer bool) {
	if s.cfg.NoAuth {
		return true, false
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return s.checkToken(r, strings.TrimPrefix(h, "Bearer ")), true
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return equal(c.Value, s.sessionValue()), false
	}
	return false, false
}

// authenticate guards /api/*: a session cookie or bearer token is required
// (except for session/login/logout), cookie-authenticated writes must carry
// the CSRF header, and READ_ONLY refuses every write.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/session", "/api/login", "/api/logout":
			if r.Method != http.MethodGet && r.Header.Get(csrfHeader) != csrfValue {
				writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		ok, bearer := s.authState(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !bearer && r.Header.Get(csrfHeader) != csrfValue {
				writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
				return
			}
			if s.cfg.ReadOnly {
				writeError(w, http.StatusForbidden, "the dashboard is in read-only mode")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	ok, _ := s.authState(r)
	resp := map[string]any{
		"authenticated": ok,
		"auth_required": !s.cfg.NoAuth,
		"read_only":     s.cfg.ReadOnly,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.NoAuth {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.checkToken(r, body.Token) {
		writeError(w, http.StatusUnauthorized, "wrong token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessionValue(),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}
