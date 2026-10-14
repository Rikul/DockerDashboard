// Command server runs the Docker Dashboard: a JSON API over the Docker
// Engine plus the embedded React frontend.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rikul/DockerDashboard/backend/internal/api"
	"github.com/Rikul/DockerDashboard/backend/internal/docker"
	"github.com/Rikul/DockerDashboard/backend/internal/registry"
	"github.com/Rikul/DockerDashboard/backend/internal/stats"
	"github.com/Rikul/DockerDashboard/backend/internal/web"
)

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	b, _ := strconv.ParseBool(os.Getenv(key))
	return b
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", env("LISTEN_ADDR", ":8080"), "address to listen on (LISTEN_ADDR)")
	flag.Parse()

	level := slog.LevelInfo
	if envBool("DEBUG") {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	token := os.Getenv("DASHBOARD_TOKEN")
	if f := os.Getenv("DASHBOARD_TOKEN_FILE"); f != "" && token == "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read DASHBOARD_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	noAuth := strings.EqualFold(os.Getenv("DASHBOARD_AUTH"), "none")
	switch {
	case token == "" && !noAuth:
		return errors.New("set DASHBOARD_TOKEN (or DASHBOARD_TOKEN_FILE) to a long random secret; " +
			"to run without sign-in on a trusted machine, set DASHBOARD_AUTH=none")
	case token != "" && len(token) < 16:
		return errors.New("DASHBOARD_TOKEN must be at least 16 characters")
	case noAuth:
		slog.Warn("sign-in is DISABLED (DASHBOARD_AUTH=none): anyone who can reach this port controls Docker on this host")
	}

	dc, err := docker.New(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := dc.Ping(pingCtx); err != nil {
		slog.Warn("docker daemon not reachable yet; will keep trying", "err", err)
	} else if v, err := dc.Version(pingCtx); err == nil {
		slog.Info("connected to docker", "version", v.Version, "api", v.APIVersion)
	}
	cancel()

	reg, err := registry.New(registry.Config{
		URL:      os.Getenv("REGISTRY_URL"),
		Username: os.Getenv("REGISTRY_USERNAME"),
		Password: os.Getenv("REGISTRY_PASSWORD"),
	})
	if err != nil {
		return err
	}

	interval, err := time.ParseDuration(env("STATS_INTERVAL", "5s"))
	if err != nil || interval < time.Second {
		return errors.New("STATS_INTERVAL must be a duration of at least 1s, e.g. 5s")
	}
	collector := stats.New(dc, interval, time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go collector.Run(ctx)

	static := web.FS()
	if static == nil {
		slog.Warn("frontend not embedded; serving the API only (run `make build` or use the Vite dev server)")
	}
	srv := api.New(api.Config{Token: token, NoAuth: noAuth, ReadOnly: envBool("READ_ONLY"), Static: static}, dc, collector, reg)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: log streams and image pulls are long-lived.
	}
	// On shutdown, end log streams (they would never finish by themselves)
	// but let in-flight actions such as a stop or pull complete.
	httpSrv.RegisterOnShutdown(srv.StopStreams)
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", *addr, "read_only", envBool("READ_ONLY"), "registry", os.Getenv("REGISTRY_URL"))
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	// Docker's default stop grace period is 10s, so finish within it.
	shutCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		// Cut off what is left, but still exit non-zero so the dirty
		// shutdown is visible to whatever supervises the process.
		_ = httpSrv.Close()
		return fmt.Errorf("requests still running at shutdown were cut off: %w", err)
	}
	return nil
}
