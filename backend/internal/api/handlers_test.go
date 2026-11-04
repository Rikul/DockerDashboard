package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rikul/DockerDashboard/backend/internal/docker"
	"github.com/Rikul/DockerDashboard/backend/internal/registry"
)

// fakeDocker serves the given JSON bodies by path; a path mapped to an int
// answers with that status instead.
func fakeDocker(t *testing.T, routes map[string]any) *docker.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if code, ok := v.(int); ok {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"message": "denied"})
			return
		}
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	dc, err := docker.New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func TestNetworksMembershipFromContainerList(t *testing.T) {
	nets := []map[string]any{
		{"Id": "n1", "Name": "app_default", "Driver": "bridge"},
		{"Id": "n2", "Name": "empty", "Driver": "bridge"},
	}
	ep := func(id string) map[string]any { return map[string]any{"NetworkID": id} }
	containers := []map[string]any{
		{"Id": "c1", "Names": []string{"/web"}, "NetworkSettings": map[string]any{"Networks": map[string]any{"app_default": ep("n1")}}},
		{"Id": "c2", "Names": []string{"/db"}, "NetworkSettings": map[string]any{"Networks": map[string]any{"app_default": ep("n1")}}},
	}
	s := &Server{docker: fakeDocker(t, map[string]any{"/networks": nets, "/containers/json": containers})}
	w := httptest.NewRecorder()
	s.handleNetworks(w, httptest.NewRequest("GET", "/api/networks", nil))
	var got []NetworkView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != 200 {
		t.Fatalf("status %d, err %v", w.Code, err)
	}
	if len(got) != 2 || strings.Join(got[0].Containers, ",") != "db,web" || got[1].Containers == nil || len(got[1].Containers) != 0 {
		t.Fatalf("bad membership: %+v", got)
	}

	// A failed container list is an error, not an empty membership.
	s = &Server{docker: fakeDocker(t, map[string]any{"/networks": nets, "/containers/json": 403})}
	w = httptest.NewRecorder()
	s.handleNetworks(w, httptest.NewRequest("GET", "/api/networks", nil))
	if w.Code != 403 {
		t.Fatalf("want the daemon's 403, got %d %s", w.Code, w.Body)
	}
}

func TestRegistryDeleteRejectsInvalidNames(t *testing.T) {
	reg, _ := registry.New(registry.Config{URL: "http://127.0.0.1:1"})
	s := &Server{registry: reg}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/registry/{repo...}", s.handleRegistryDelete)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/registry/team/app?tag=..%2F..%2Fother", nil))
	if w.Code != 400 {
		t.Fatalf("want 400, got %d %s", w.Code, w.Body)
	}
}

func TestContainerLogsUnreadableDriver(t *testing.T) {
	// The daemon answers 501 for --log-driver none. EventSource can't read a
	// failed response, so the reason must arrive as an "end" event on a 200.
	inspect := map[string]any{"Id": "c1", "HostConfig": map[string]any{"LogConfig": map[string]any{"Type": "none"}}}
	s := &Server{docker: fakeDocker(t, map[string]any{"/containers/c1/json": inspect, "/containers/c1/logs": 501})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}/logs", s.handleContainerLogs)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/c1/logs?follow=0", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("want a 200 event stream, got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if b := w.Body.String(); !strings.HasPrefix(b, "event: end\n") || !strings.Contains(b, "logging driver (none)") {
		t.Fatalf("want an end event naming the driver, got %q", b)
	}
}

func TestVolumeInspectRedactsCredentials(t *testing.T) {
	vol := map[string]any{
		"Name": "share", "Driver": "local",
		"Options": map[string]any{"type": "cifs", "device": "//nas/share", "o": "addr=nas,username=bob,password=hunter2,vers=3.0", "passphrase": "x"},
	}
	s := &Server{docker: fakeDocker(t, map[string]any{"/volumes/share": vol})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/volumes/{name}", s.handleVolumeInspect)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/volumes/share", nil))
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	b := w.Body.String()
	if strings.Contains(b, "hunter2") || !strings.Contains(b, "password=REDACTED") || !strings.Contains(b, `"passphrase":"REDACTED"`) {
		t.Fatalf("credentials not redacted: %s", b)
	}
	if !strings.Contains(b, "username=bob") || !strings.Contains(b, "//nas/share") {
		t.Fatalf("non-secret options lost: %s", b)
	}
}

func TestNetworkInspect(t *testing.T) {
	net := map[string]any{"Id": "n1", "Name": "app_default", "Driver": "bridge"}
	s := &Server{docker: fakeDocker(t, map[string]any{"/networks/n1": net})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/networks/{id}", s.handleNetworkInspect)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/networks/n1", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Name":"app_default"`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/networks/gone", nil))
	if w.Code != 404 {
		t.Fatalf("want the daemon's 404, got %d %s", w.Code, w.Body)
	}
}

func TestContainerTop(t *testing.T) {
	top := map[string]any{"Titles": []string{"PID", "CMD"}, "Processes": [][]string{{"1", "nginx"}, {"7", "sh"}}}
	s := &Server{docker: fakeDocker(t, map[string]any{"/containers/c1/top": top, "/containers/c2/top": 409})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}/top", s.handleContainerTop)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/c1/top", nil))
	var got struct {
		Titles    []string   `json:"titles"`
		Processes [][]string `json:"processes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != 200 {
		t.Fatalf("status %d, err %v", w.Code, err)
	}
	if strings.Join(got.Titles, ",") != "PID,CMD" || len(got.Processes) != 2 || got.Processes[1][1] != "sh" {
		t.Fatalf("bad top: %+v", got)
	}
	// A container that isn't running is the daemon's 409, not a 502.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/c2/top", nil))
	if w.Code != 409 {
		t.Fatalf("want the daemon's 409, got %d %s", w.Code, w.Body)
	}
}

func TestContainerDiff(t *testing.T) {
	changes := []map[string]any{{"Path": "/var/log", "Kind": 0}, {"Path": "/tmp/new", "Kind": 1}, {"Path": "/etc/gone", "Kind": 2}}
	s := &Server{docker: fakeDocker(t, map[string]any{"/containers/c1/changes": changes, "/containers/c2/changes": nil})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}/diff", s.handleContainerDiff)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/c1/diff", nil))
	var got []ChangeView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != 200 {
		t.Fatalf("status %d, err %v", w.Code, err)
	}
	want := []ChangeView{{"/etc/gone", "deleted"}, {"/tmp/new", "added"}, {"/var/log", "modified"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	// No changes is an empty list, never null.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/c2/diff", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("want [], got %d %q", w.Code, w.Body)
	}
}

func TestContainerRename(t *testing.T) {
	var gotMethod, gotPath, gotName string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotName = r.Method, r.URL.Path, r.URL.Query().Get("name")
		w.WriteHeader(status)
		if status >= 400 {
			json.NewEncoder(w).Encode(map[string]string{"message": "Conflict. The container name is already in use"})
		}
	}))
	t.Cleanup(srv.Close)
	dc, err := docker.New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{docker: dc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/containers/{id}/rename", s.handleContainerRename)
	rename := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/containers/c1/rename", strings.NewReader(body)))
		return w
	}

	w := rename(`{"name": " web-2 "}`)
	if w.Code != 200 || gotMethod != "POST" || gotPath != "/containers/c1/rename" || gotName != "web-2" {
		t.Fatalf("status %d; daemon saw %s %s name=%q", w.Code, gotMethod, gotPath, gotName)
	}

	// Names the daemon would refuse never reach it.
	gotPath = ""
	for _, bad := range []string{`{"name": ""}`, `{"name": "a"}`, `{"name": "-web"}`, `{"name": "a b"}`, `{"name": "a/b"}`, `{"nom": "x"}`, `nope`} {
		if w := rename(bad); w.Code != 400 {
			t.Errorf("%s: want 400, got %d", bad, w.Code)
		}
	}
	if gotPath != "" {
		t.Fatalf("an invalid name reached the daemon: %s", gotPath)
	}

	// A name already in use is the daemon's 409 with its message.
	status = http.StatusConflict
	if w := rename(`{"name": "taken"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("want the daemon's 409, got %d %s", w.Code, w.Body)
	}
}
