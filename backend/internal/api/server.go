// Package api serves the dashboard's JSON API and the built frontend.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Rikul/DockerDashboard/backend/internal/docker"
	"github.com/Rikul/DockerDashboard/backend/internal/registry"
	"github.com/Rikul/DockerDashboard/backend/internal/stats"
)

// Config controls the server's behaviour.
type Config struct {
	// Token is the shared secret users log in with (required).
	Token    string
	ReadOnly bool
	// Static is the built frontend; nil serves the API only.
	Static fs.FS
}

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	cfg      Config
	docker   *docker.Client
	stats    *stats.Collector
	registry *registry.Client // nil when no registry is configured

	inspectMu    sync.Mutex
	inspectCache map[string]inspectEntry

	dfMu     sync.Mutex
	dfCache  *docker.DiskUsage
	dfCached time.Time

	loginMu       sync.Mutex
	loginAttempts map[string]*attempts // by client address
	loginPruned   time.Time
	failStep      time.Duration // wait per recent failure; 0 means the default

	sessionMu sync.Mutex
	revoked   map[string]time.Time // signed-out session id -> its expiry

	// streams is cancelled when shutdown begins, ending long-lived log
	// streams while ordinary requests are left to finish.
	streams     context.Context
	stopStreams context.CancelFunc
}

type inspectEntry struct {
	state string
	at    time.Time
	data  docker.ContainerJSON
}

// New wires a server. reg may be nil.
func New(cfg Config, dc *docker.Client, sc *stats.Collector, reg *registry.Client) *Server {
	streams, stop := context.WithCancel(context.Background())
	return &Server{cfg: cfg, docker: dc, stats: sc, registry: reg, inspectCache: map[string]inspectEntry{}, streams: streams, stopStreams: stop}
}

// StopStreams ends open log streams. Call it when shutdown begins, so
// http.Server.Shutdown is not held open by them.
func (s *Server) StopStreams() {
	if s.stopStreams != nil {
		s.stopStreams()
	}
}

// streamCtx is cancelled when shutdown begins. It is never cancelled for a
// Server not built by New.
func (s *Server) streamCtx() context.Context {
	if s.streams == nil {
		return context.Background()
	}
	return s.streams
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Session
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Host
	mux.HandleFunc("GET /api/system", s.handleSystem)
	mux.HandleFunc("GET /api/stats", s.handleStats)

	// Containers
	mux.HandleFunc("GET /api/containers", s.handleContainers)
	mux.HandleFunc("GET /api/containers/{id}", s.handleContainerInspect)
	mux.HandleFunc("GET /api/containers/{id}/logs", s.handleContainerLogs)
	mux.HandleFunc("POST /api/containers/{id}/{action}", s.handleContainerAction)
	mux.HandleFunc("DELETE /api/containers/{id}", s.handleContainerRemove)
	mux.HandleFunc("POST /api/containers/prune", s.handleContainersPrune)

	// Images
	mux.HandleFunc("GET /api/images", s.handleImages)
	mux.HandleFunc("GET /api/images/{id}", s.handleImageInspect)
	mux.HandleFunc("DELETE /api/images/{id}", s.handleImageRemove)
	mux.HandleFunc("POST /api/images/pull", s.handleImagePull)
	mux.HandleFunc("POST /api/images/prune", s.handleImagesPrune)

	// Volumes
	mux.HandleFunc("GET /api/volumes", s.handleVolumes)
	mux.HandleFunc("GET /api/volumes/{name}", s.handleVolumeInspect)
	mux.HandleFunc("POST /api/volumes", s.handleVolumeCreate)
	mux.HandleFunc("DELETE /api/volumes/{name}", s.handleVolumeRemove)
	mux.HandleFunc("POST /api/volumes/prune", s.handleVolumesPrune)

	// Networks
	mux.HandleFunc("GET /api/networks", s.handleNetworks)
	mux.HandleFunc("POST /api/networks", s.handleNetworkCreate)
	mux.HandleFunc("DELETE /api/networks/{id}", s.handleNetworkRemove)
	mux.HandleFunc("POST /api/networks/{id}/connect", s.handleNetworkConnect)
	mux.HandleFunc("POST /api/networks/{id}/disconnect", s.handleNetworkDisconnect)
	mux.HandleFunc("POST /api/networks/prune", s.handleNetworksPrune)

	// Registry
	mux.HandleFunc("GET /api/registry", s.handleRegistry)
	mux.HandleFunc("DELETE /api/registry/{repo...}", s.handleRegistryDelete)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	if s.cfg.Static != nil {
		mux.Handle("/", spa(s.cfg.Static))
	}
	return securityHeaders(logRequests(s.authenticate(mux)))
}

// ---- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// dockerError maps a daemon error to a response, keeping 4xx statuses
// (not found, conflict, in use) so the UI can explain them.
func dockerError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	code := docker.StatusCode(err)
	var de *docker.Error
	switch {
	case code >= 400 && code < 500 && errors.As(err, &de):
		writeError(w, code, de.Message)
	default:
		slog.Error("docker call failed", "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func decodeBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(r.URL.Query().Get(key)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && (r.Method != http.MethodGet || rec.status >= 400) {
			slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start).Round(time.Millisecond))
		}
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// spa serves the built frontend, falling back to index.html for client routes.
func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(static, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			p = "index.html"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
