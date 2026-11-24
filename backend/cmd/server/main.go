// Command server runs the Docker Dashboard: a JSON API over the Docker
// Engine plus the embedded React frontend.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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

func envBool(key string, def bool) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return b
}

// loadToken returns the sign-in token: DASHBOARD_TOKEN, else the contents of
// DASHBOARD_TOKEN_FILE. With neither, it generates a random token (generated
// is true) and, when a file path is set but the file doesn't exist yet, saves
// it there so the same token is used after a restart.
func loadToken(token, file string) (_ string, generated bool, _ error) {
	if token == "" && file != "" {
		b, err := os.ReadFile(file)
		switch {
		case err == nil:
			token = strings.TrimSpace(string(b))
			if token == "" {
				return "", false, fmt.Errorf("DASHBOARD_TOKEN_FILE %s is empty", file)
			}
		case errors.Is(err, fs.ErrNotExist):
			// Generated below and saved here.
		default:
			return "", false, fmt.Errorf("read DASHBOARD_TOKEN_FILE: %w", err)
		}
	}
	if token == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return "", false, fmt.Errorf("generate token: %w", err)
		}
		token = hex.EncodeToString(buf)
		if file != "" {
			if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
				return "", false, fmt.Errorf("save generated token to DASHBOARD_TOKEN_FILE: %w", err)
			}
		}
		return token, true, nil
	}
	if len(token) < 16 {
		return "", false, errors.New("DASHBOARD_TOKEN must be at least 16 characters")
	}
	return token, false, nil
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
	if envBool("DEBUG", false) {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	token, generated, err := loadToken(os.Getenv("DASHBOARD_TOKEN"), os.Getenv("DASHBOARD_TOKEN_FILE"))
	if err != nil {
		return err
	}
	if generated {
		// Printed on its own lines so it is easy to find in `docker compose logs`.
		fmt.Fprintf(os.Stderr, "\n  Generated a sign-in token: %s\n\n", token)
		if f := os.Getenv("DASHBOARD_TOKEN_FILE"); f != "" {
			slog.Info("saved the generated token; it is reused on restart", "file", f)
		} else {
			slog.Warn("the generated token changes on every restart; set DASHBOARD_TOKEN or DASHBOARD_TOKEN_FILE to keep it")
		}
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

	readOnly := envBool("READ_ONLY", true)
	static := web.FS()
	if static == nil {
		slog.Warn("frontend not embedded; serving the API only (run `make build` or use the Vite dev server)")
	}
	srv := api.New(api.Config{Token: token, ReadOnly: readOnly, Static: static}, dc, collector, reg)

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
		slog.Info("listening", "addr", *addr, "read_only", readOnly, "registry", os.Getenv("REGISTRY_URL"))
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
