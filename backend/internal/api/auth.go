package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/netip"
	"strconv"
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

// A session cookie is "v2.<expiry unix>.<random id>.<hmac>", signed with
// a key derived from the token. The server checks the signature and the
// expiry, so a copied cookie stops working after sessionTTL and changing the
// token signs everyone out; the token itself never sits in a cookie. Logout
// revokes the session's id. Revoked ids are kept in memory (a restart
// forgets revocations, not sessions) and those past their expiry are
// dropped at the next logout; an expired entry is never consulted, since
// parseSession already rejects the cookie.
const sessionPrefix = "v2"

func (s *Server) sessionMAC(payload string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.Token))
	m.Write([]byte("dockerdashboard-session-v2\x00" + payload))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) newSession(now time.Time) string {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	payload := fmt.Sprintf("%s.%d.%s", sessionPrefix, now.Add(sessionTTL).Unix(), hex.EncodeToString(id))
	return payload + "." + s.sessionMAC(payload)
}

// parseSession returns the session's id and expiry if v is a validly
// signed, unexpired session.
func (s *Server) parseSession(v string, now time.Time) (id string, exp time.Time, ok bool) {
	i := strings.LastIndexByte(v, '.')
	if i < 0 {
		return "", time.Time{}, false
	}
	payload, mac := v[:i], v[i+1:]
	if !equal(mac, s.sessionMAC(payload)) {
		return "", time.Time{}, false
	}
	parts := strings.Split(payload, ".")
	if len(parts) != 3 || parts[0] != sessionPrefix {
		return "", time.Time{}, false
	}
	sec, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	exp = time.Unix(sec, 0)
	if !now.Before(exp) {
		return "", time.Time{}, false
	}
	return parts[2], exp, true
}

func (s *Server) sessionValid(v string) bool {
	id, _, ok := s.parseSession(v, time.Now())
	if !ok {
		return false
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	_, revoked := s.revoked[id]
	return !revoked
}

// revokeSession remembers a signed-out session until it would expire.
func (s *Server) revokeSession(v string) {
	now := time.Now()
	id, exp, ok := s.parseSession(v, now)
	if !ok {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.revoked == nil {
		s.revoked = map[string]time.Time{}
	}
	for k, e := range s.revoked {
		if !now.Before(e) {
			delete(s.revoked, k)
		}
	}
	s.revoked[id] = exp
}

// Guessing the token is slowed down per client address: after repeated
// failures from one address, each further attempt from it waits before it is
// answered, and attempts from one address are checked one at a time so
// sending many in parallel does not get around the wait. Other clients, and
// successful requests elsewhere, neither reset nor inherit that wait.
const (
	defaultFailStep = 500 * time.Millisecond
	failWindow      = 15 * time.Minute
	maxFailSteps    = 10
	maxTracked      = 4096 // addresses remembered before old entries are pruned
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
		// Still full of recent failures: rather than growing the map, new
		// addresses go to one of a few shared buckets chosen by hash. A
		// new client may then share a bucket (and its wait) with an
		// attacker, but only one in overflowBuckets of them does.
		h := fnv.New32a()
		h.Write([]byte(addr))
		addr = fmt.Sprintf("overflow-%d", h.Sum32()%overflowBuckets)
		if a := s.loginAttempts[addr]; a != nil {
			return a
		}
	}
	a := &attempts{}
	s.loginAttempts[addr] = a
	return a
}

// overflowBuckets is how many shared buckets new addresses are spread over
// once maxTracked addresses are tracked. Their keys are not valid
// addresses, so no client maps to one directly.
const overflowBuckets = 64

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

func (s *Server) failDelayStep() time.Duration {
	if s.failStep > 0 {
		return s.failStep
	}
	return defaultFailStep
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
		t := time.NewTimer(time.Duration(min(a.failures, maxFailSteps)) * s.failDelayStep())
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
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return s.checkToken(r, strings.TrimPrefix(h, "Bearer ")), true
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return s.sessionValid(c.Value), false
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
		"read_only":     s.cfg.ReadOnly,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
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
		Value:    s.newSession(time.Now()),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.revokeSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}
