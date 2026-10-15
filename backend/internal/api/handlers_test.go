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
