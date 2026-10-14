package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
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

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// authState reports whether r is signed in, and how.
func (s *Server) authState(r *http.Request) (ok, bearer bool) {
	if s.cfg.NoAuth {
		return true, false
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return equal(strings.TrimPrefix(h, "Bearer "), s.cfg.Token), true
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
	// Slow down guessing: after repeated failures, wait before answering.
	s.loginMu.Lock()
	if time.Since(s.loginLastFail) > 15*time.Minute {
		s.loginFailures = 0
	}
	delay := time.Duration(min(s.loginFailures, 10)) * 500 * time.Millisecond
	s.loginMu.Unlock()
	time.Sleep(delay)

	if !equal(body.Token, s.cfg.Token) {
		s.loginMu.Lock()
		s.loginFailures++
		s.loginLastFail = time.Now()
		s.loginMu.Unlock()
		writeError(w, http.StatusUnauthorized, "wrong token")
		return
	}
	s.loginMu.Lock()
	s.loginFailures = 0
	s.loginMu.Unlock()
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
