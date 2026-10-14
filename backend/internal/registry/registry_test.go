package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	l, err := c.List(t.Context(), time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	tags := l.Tags
	if len(l.Problems) != 0 {
		t.Fatalf("unexpected problems %v", l.Problems)
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

func TestTagsFollowsPaginationAndRanksByCreation(t *testing.T) {
	// Three pages of tags; v9 is the newest image even though "v9" sorts
	// after "v10" and v100 lexically, and failing repos are reported.
	created := map[string]string{"v1": "2026-01-01T00:00:00Z", "v9": "2026-09-01T00:00:00Z", "v10": "2026-03-01T00:00:00Z", "v100": "2026-02-01T00:00:00Z"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"app", "broken"}})
		case r.URL.Path == "/v2/app/tags/list":
			switch r.URL.Query().Get("last") {
			case "":
				w.Header().Set("Link", `</v2/app/tags/list?n=2&last=v10>; rel="next"`)
				json.NewEncoder(w).Encode(map[string]any{"tags": []string{"v1", "v10"}})
			case "v10":
				w.Header().Set("Link", `<`+"http://"+r.Host+`/v2/app/tags/list?n=2&last=v100>; rel="next"`)
				json.NewEncoder(w).Encode(map[string]any{"tags": []string{"v100"}})
			default:
				json.NewEncoder(w).Encode(map[string]any{"tags": []string{"v9"}})
			}
		case r.URL.Path == "/v2/broken/tags/list":
			w.WriteHeader(500)
		case strings.HasPrefix(r.URL.Path, "/v2/app/manifests/"):
			tag := strings.TrimPrefix(r.URL.Path, "/v2/app/manifests/")
			json.NewEncoder(w).Encode(map[string]any{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config":    map[string]any{"digest": "sha256:cfg-" + tag, "size": 1},
			})
		case strings.HasPrefix(r.URL.Path, "/v2/app/blobs/sha256:cfg-"):
			json.NewEncoder(w).Encode(map[string]any{"created": created[strings.TrimPrefix(r.URL.Path, "/v2/app/blobs/sha256:cfg-")]})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, MaxTags: 2})
	l, err := c.List(t.Context(), time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tags) != 2 || l.Tags[0].Tag != "v9" || l.Tags[1].Tag != "v10" {
		t.Fatalf("want v9, v10; got %+v", l.Tags)
	}
	if len(l.Problems) != 1 || !strings.HasPrefix(l.Problems[0], "broken:") {
		t.Fatalf("want a problem for the broken repo, got %v", l.Problems)
	}
	if c.cacheComplete {
		t.Fatal("an incomplete listing must not be cached as complete")
	}
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct{ a, b string }{{"v9", "v10"}, {"v10", "v100"}, {"1.2", "1.10"}, {"a", "b"}} {
		if !versionLess(tc.a, tc.b) || versionLess(tc.b, tc.a) {
			t.Errorf("want %q < %q", tc.a, tc.b)
		}
	}
}

func TestTransientDescribeFailureIsNotCached(t *testing.T) {
	var fail, blobFail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"app"}})
		case r.URL.Path == "/v2/app/tags/list":
			json.NewEncoder(w).Encode(map[string]any{"tags": []string{"v1"}})
		case r.URL.Path == "/v2/app/manifests/v1" && fail:
			w.WriteHeader(503)
		case r.URL.Path == "/v2/app/manifests/v1":
			json.NewEncoder(w).Encode(map[string]any{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config":    map[string]any{"digest": "sha256:cfg", "size": 1},
			})
		case r.URL.Path == "/v2/app/blobs/sha256:cfg" && blobFail:
			w.WriteHeader(503)
		case r.URL.Path == "/v2/app/blobs/sha256:cfg":
			json.NewEncoder(w).Encode(map[string]any{"created": "2026-10-01T12:00:00Z"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL})
	expire := func() { c.cachedAt = c.cachedAt.Add(-failedListingTTL - time.Second) }

	fail = true
	l, err := c.List(t.Context(), time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tags) != 1 || l.Tags[0].Error == "" || len(l.Problems) == 0 || c.cacheComplete {
		t.Fatalf("want an unreadable tag, a problem and an incomplete cache, got %+v", l)
	}
	// Within failedListingTTL the failed listing (with its problem) is
	// served without rescanning; after it, the registry is tried again.
	fail = false
	if l, _ = c.List(t.Context(), time.Minute, false); len(l.Problems) == 0 {
		t.Fatal("want the recent failed listing served from cache")
	}
	expire()
	if l, _ = c.List(t.Context(), time.Minute, false); l.Tags[0].Error != "" || !c.cacheComplete {
		t.Fatalf("the failed listing was not retried: %+v", l.Tags[0])
	}

	// A forced refresh that fails replaces the older good listing.
	fail = true
	c.List(t.Context(), time.Minute, true)
	if l, _ = c.List(t.Context(), time.Minute, false); l.Tags[0].Error == "" {
		t.Fatal("a failed refresh left the previous listing in place")
	}

	// A failed config-blob read keeps the tag readable but the listing
	// incomplete.
	fail, blobFail = false, true
	expire()
	l, _ = c.List(t.Context(), time.Minute, false)
	if l.Tags[0].Error != "" || l.Tags[0].Digest == "" && l.Tags[0].Size == 0 || c.cacheComplete {
		t.Fatalf("want a readable tag in an incomplete listing, got %+v complete=%v", l.Tags[0], c.cacheComplete)
	}
}

func TestCatalogPaginationAndTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/_catalog?n=2&last=b>; rel="next"`)
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"a", "b"}})
		case r.URL.Path == "/v2/_catalog":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"c"}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"tags": []string{}})
		}
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, MaxRepos: 3})
	if repos, more, err := c.repositories(t.Context()); err != nil || more || len(repos) != 3 {
		t.Fatalf("want all 3 repositories across pages, got %v more=%v err=%v", repos, more, err)
	}
	c, _ = New(Config{URL: srv.URL, MaxRepos: 2})
	l, err := c.List(t.Context(), time.Minute, false)
	if err != nil || len(l.Problems) != 1 || !strings.Contains(l.Problems[0], "more than 2 repositories") {
		t.Fatalf("want a truncation problem, got %+v err=%v", l, err)
	}
}

func TestTokenRealmWithQuery(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			q := r.URL.Query()
			if q.Get("account") != "x" || q.Get("service") != "svc" || q.Get("scope") == "" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "t"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token?account=x",service="svc"`, srv.URL))
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"a"}})
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL})
	if repos, err := c.Repositories(t.Context()); err != nil || len(repos) != 1 {
		t.Fatalf("got %v, %v", repos, err)
	}
}

func TestDeleteTagRejectsInvalidNames(t *testing.T) {
	c, _ := New(Config{URL: "http://127.0.0.1:1"})
	for _, tc := range [][2]string{{"foo?x=1", "v1"}, {"team/app", "../../other/manifests/latest"}, {"Team/App", "v1"}, {"app", ""}} {
		if err := c.DeleteTag(t.Context(), tc[0], tc[1]); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q:%q: want ErrInvalidName, got %v", tc[0], tc[1], err)
		}
	}
}

func TestConcurrentListsShareOneScan(t *testing.T) {
	var catalogs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/_catalog" {
			catalogs.Add(1)
			time.Sleep(50 * time.Millisecond)
			json.NewEncoder(w).Encode(map[string]any{"repositories": []string{}})
		}
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL})
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); c.List(t.Context(), time.Minute, false) }()
	}
	wg.Wait()
	if n := catalogs.Load(); n != 1 {
		t.Fatalf("want one scan for concurrent requests, got %d", n)
	}
}
