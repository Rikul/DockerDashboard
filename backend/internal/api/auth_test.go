package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testServer(cfg Config) http.Handler {
	s := &Server{cfg: cfg}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.Handle("/api/", ok)
	return s.authenticate(mux)
}

func do(h http.Handler, method, path, body string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const tok = "0123456789abcdef-token"

func TestAuth(t *testing.T) {
	h := testServer(Config{Token: tok})
	csrf := map[string]string{csrfHeader: csrfValue}

	if w := do(h, "GET", "/api/containers", "", nil); w.Code != 401 {
		t.Fatalf("anonymous read: %d", w.Code)
	}
	if w := do(h, "GET", "/api/containers", "", map[string]string{"Authorization": "Bearer " + tok}); w.Code != http.StatusTeapot {
		t.Fatalf("bearer read: %d", w.Code)
	}
	if w := do(h, "POST", "/api/login", `{"token":"`+tok+`"}`, nil); w.Code != 403 {
		t.Fatalf("login without CSRF header: %d", w.Code)
	}
	w := do(h, "POST", "/api/login", `{"token":"`+tok+`"}`, csrf)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Value == tok || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("bad cookie %+v", cookie)
	}
	if w := do(h, "GET", "/api/containers", "", nil, cookie); w.Code != http.StatusTeapot {
		t.Fatalf("cookie read: %d", w.Code)
	}
	if w := do(h, "POST", "/api/containers/x/stop", "", nil, cookie); w.Code != 403 {
		t.Fatalf("cookie write without CSRF header: %d", w.Code)
	}
	if w := do(h, "POST", "/api/containers/x/stop", "", csrf, cookie); w.Code != http.StatusTeapot {
		t.Fatalf("cookie write: %d", w.Code)
	}
	forged := &http.Cookie{Name: sessionCookie, Value: "forged"}
	if w := do(h, "GET", "/api/containers", "", nil, forged); w.Code != 401 {
		t.Fatalf("forged cookie: %d", w.Code)
	}
}

func TestReadOnly(t *testing.T) {
	h := testServer(Config{Token: tok, ReadOnly: true})
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	if w := do(h, "GET", "/api/containers", "", bearer); w.Code != http.StatusTeapot {
		t.Fatalf("read: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/volumes/x", "", bearer); w.Code != 403 || !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("write in read-only mode: %d %s", w.Code, w.Body)
	}
}

func TestHealthFromStatus(t *testing.T) {
	for in, want := range map[string]string{
		"Up 2 hours (healthy)":            "healthy",
		"Up 5 seconds (health: starting)": "starting",
		"Up 3 days (unhealthy)":           "unhealthy",
		"Exited (0) 2 minutes ago":        "",
	} {
		if got := healthFromStatus(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestThrottlingIsPerClientAndSerialized(t *testing.T) {
	const step = 100 * time.Millisecond
	s := &Server{cfg: Config{Token: tok}, failStep: step}
	h := s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	from := func(addr, token string) int {
		r := httptest.NewRequest("GET", "/api/containers", nil)
		r.RemoteAddr = addr + ":5555"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for range 2 {
		if c := from("198.51.100.7", "wrong"); c != 401 {
			t.Fatalf("wrong bearer: %d", c)
		}
	}
	if n := s.attemptsFor("198.51.100.7").failures; n != 2 {
		t.Fatalf("bearer failures not counted: %d", n)
	}
	// Another client's success neither resets the attacker's count nor
	// waits behind it.
	start := time.Now()
	if c := from("203.0.113.9", tok); c != 200 {
		t.Fatalf("good bearer: %d", c)
	}
	if time.Since(start) > step/2 {
		t.Fatal("a clean client should not be delayed by another client's failures")
	}
	if n := s.attemptsFor("198.51.100.7").failures; n != 2 {
		t.Fatalf("another client's success reset the count: %d", n)
	}
	// Parallel guesses from one address are checked one at a time, so the
	// waits add up instead of overlapping: 2+3+4 steps at least.
	start = time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); from("198.51.100.7", "wrong") }()
	}
	wg.Wait()
	if el := time.Since(start); el < 9*step {
		t.Fatalf("parallel guesses were not serialized: took %v", el)
	}
}

func TestRedactImageInspect(t *testing.T) {
	m, err := redactImageInspect([]byte(`{"Id":"sha256:x","Config":{"Env":["SECRET=1"],"Cmd":["sh"]},"ContainerConfig":{"Env":["SECRET=1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["Config"].(map[string]any)["Env"]; ok {
		t.Fatal("Config.Env not removed")
	}
	if _, ok := m["ContainerConfig"].(map[string]any)["Env"]; ok {
		t.Fatal("ContainerConfig.Env not removed")
	}
	if m["Config"].(map[string]any)["Cmd"] == nil {
		t.Fatal("other fields should be kept")
	}
}

func TestTrackedAddressesAreBounded(t *testing.T) {
	s := &Server{}
	for i := range maxTracked + 50 {
		a := s.attemptsFor(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
		a.failures, a.lastFail = 1, time.Now()
	}
	// New addresses are spread over the overflow buckets, so the map
	// stays bounded and one bucket holds only part of them.
	buckets := map[*attempts]bool{}
	for i := range 1000 {
		buckets[s.attemptsFor(fmt.Sprintf("192.0.%d.%d", i/256, i%256))] = true
	}
	if len(buckets) < overflowBuckets/2 || len(buckets) > overflowBuckets {
		t.Fatalf("new addresses landed in %d buckets, want about %d", len(buckets), overflowBuckets)
	}
	if n := len(s.loginAttempts); n > maxTracked+overflowBuckets {
		t.Fatalf("tracked %d entries, want at most %d", n, maxTracked+overflowBuckets)
	}
}

func TestClientAddrGroupsIPv6By64(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8:1:2:aaaa::1]:443"
	a := clientAddr(r)
	r.RemoteAddr = "[2001:db8:1:2:bbbb::9]:443"
	if b := clientAddr(r); a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("got %q and %q", a, b)
	}
	r.RemoteAddr = "192.0.2.4:80"
	if c := clientAddr(r); c != "192.0.2.4" {
		t.Fatalf("ipv4: %q", c)
	}
}
