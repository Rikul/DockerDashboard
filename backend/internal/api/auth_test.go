package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
