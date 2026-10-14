package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRegistry requires a bearer token from its /token endpoint, like
// Docker Hub, Harbor or GHCR do.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			u, p, _ := r.BasicAuth()
			if u != "bob" || p != "secret" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "tok-" + r.URL.Query().Get("scope")})
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, srv.URL))
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v2/_catalog":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"team/app"}})
		case "/v2/team/app/tags/list":
			json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.0", "2.0"}})
		case "/v2/team/app/manifests/1.0", "/v2/team/app/manifests/2.0":
			if r.Method == http.MethodDelete {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:m1")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			json.NewEncoder(w).Encode(map[string]any{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config":    map[string]any{"digest": "sha256:cfg", "size": 100},
				"layers":    []map[string]any{{"digest": "sha256:l1", "size": 1000}, {"digest": "sha256:l2", "size": 24}},
			})
		case "/v2/team/app/manifests/sha256:m1":
			if r.Method != http.MethodDelete {
				t.Errorf("unexpected %s", r.Method)
			}
			w.WriteHeader(http.StatusAccepted)
		case "/v2/team/app/blobs/sha256:cfg":
			json.NewEncoder(w).Encode(map[string]any{"created": "2026-10-01T12:00:00Z"})
		default:
			w.WriteHeader(404)
		}
	}))
	return srv
}

func TestListAndDeleteWithBearerAuth(t *testing.T) {
	srv := fakeRegistry(t)
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Username: "bob", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	tags, err := c.List(t.Context(), time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Tag != "2.0" {
		t.Fatalf("want 2 tags newest first, got %+v", tags)
	}
	got := tags[1]
	if got.Error != "" || got.Size != 1124 || got.Digest != "sha256:m1" || got.Created.Year() != 2026 {
		t.Fatalf("bad tag %+v", got)
	}
	if !strings.HasSuffix(got.Ref, "/team/app:1.0") {
		t.Fatalf("bad ref %q", got.Ref)
	}
	if err := c.DeleteTag(t.Context(), "team/app", "1.0"); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := fakeRegistry(t)
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, Username: "bob", Password: "wrong"})
	if _, err := c.Repositories(t.Context()); err == nil {
		t.Fatal("want an error")
	}
}

func TestParseChallenge(t *testing.T) {
	p := parseChallenge(`realm="https://auth.example.com/token",service="registry.example.com",scope="repository:a/b:pull,push"`)
	if p["realm"] != "https://auth.example.com/token" || p["service"] != "registry.example.com" || p["scope"] != "repository:a/b:pull,push" {
		t.Fatalf("got %v", p)
	}
}

func TestDisabledWithoutURL(t *testing.T) {
	c, err := New(Config{})
	if c != nil || err != nil {
		t.Fatal("empty URL should disable the registry")
	}
}
