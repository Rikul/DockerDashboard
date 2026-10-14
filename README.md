# Docker Dashboard

A self-hosted web dashboard for one Docker host. It covers containers, images, volumes and networks, plus a private registry.

- **Overview**: running and stopped containers, a list of what needs attention (restart loops, failing health checks, crashes, OOM kills, memory near its limit), disk use, reclaimable space, CPU over the last hour and memory against limits.
- **Containers**: search, filter and sort. Start, stop, restart, pause, remove, live logs (follow, filter, download) and inspect.
- **Images**: size, age, which are in use, unused and dangling. Pull, update a tag, remove, prune.
- **Volumes**: size, which container mounts each one, anonymous vs named. Create, delete, prune.
- **Networks**: subnet, gateway, attached containers. Create, connect and disconnect containers, remove, prune.
- **Registry**: tags in any Registry API v2 registry (distribution, Harbor, GitLab, GHCR…) with size, build date and platforms. Pull to the host, delete a tag.

**Stack:** Go (standard library only, no third-party modules) and React 19 + TypeScript (Vite, d3 for charts). Everything ships as one static binary with the frontend embedded, in a distroless image.

## Quick start

```sh
echo "DASHBOARD_TOKEN=$(openssl rand -hex 24)" > .env
docker compose up -d --build
```

Open <http://localhost:8080> and sign in with the token from `.env`.

## Development

Requirements: Go 1.24+, Node 22+, and a Docker daemon.

```sh
make dev     # Go API on 127.0.0.1:8080 + Vite on :5173 (proxying /api). Token: dev-token-change-me
make test    # go vet, go test -race, TypeScript type-check
make build   # bin/docker-dashboard with the frontend embedded
```

Running the backend on its own without building the frontend first serves the API only and logs a warning.

## Configuration

| Variable | Default | |
|---|---|---|
| `DASHBOARD_TOKEN` | — (required) | Shared secret for signing in, at least 16 characters. Or set `DASHBOARD_TOKEN_FILE` to read it from a file (Docker secrets). |
| `DASHBOARD_AUTH` | | Set to `none` to disable sign-in. Only do this on a machine nobody else can reach. |
| `READ_ONLY` | `false` | `true` refuses every change. Viewing and logs still work. |
| `LISTEN_ADDR` | `:8080` | Address the server listens on. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | `unix://…` or `tcp://host:2375`. TLS to the daemon isn't supported yet. |
| `STATS_INTERVAL` | `5s` | How often CPU and memory are sampled. One hour of history is kept in memory. |
| `REGISTRY_URL` | | Registry for the Registry tab, e.g. `https://registry.example.com`. |
| `REGISTRY_USERNAME` / `REGISTRY_PASSWORD` | | Registry credentials. Basic and bearer-token auth both work. They are also passed to the daemon when pulling from that registry. |
| `DEBUG` | `false` | Verbose logs. |

## Security

**Access to the Docker socket is root on the host.** Anyone who can sign in to this dashboard can, in effect, control the machine. Defaults are set with that in mind:

- Sign-in is required unless you explicitly set `DASHBOARD_AUTH=none`.
- The session is an `HttpOnly`, `SameSite=Strict` cookie derived from the token, so the token itself is never stored in the browser. Changing the token signs everyone out.
- Every write must carry an `X-Requested-With` header, which protects against cross-site requests. Repeated wrong tokens (at login or as a bearer token) are answered more and more slowly, counted per client address, and one address's attempts are checked one at a time. Behind a reverse proxy every client shares the proxy's address, so they share that slowdown.
- Responses carry a strict Content-Security-Policy, `X-Frame-Options: DENY` and `nosniff`.
- Container environment variables are never sent to the browser.
- Compose binds the port to `127.0.0.1`.

Before exposing it to a network:

1. Put it behind HTTPS (Caddy, Traefik, nginx). The cookie gets `Secure` when `X-Forwarded-Proto: https` is set.
2. Use `READ_ONLY=true` for viewers, or run a second, read-only instance for them.
3. Consider the [socket proxy](docker-compose.yml) setup, which limits which Docker API calls are possible at all. Mounting the socket `:ro` does **not** do this.

Scripts can call the API with `Authorization: Bearer $DASHBOARD_TOKEN`.

## API

All endpoints are under `/api` and return JSON.

| Method | Path | |
|---|---|---|
| GET | `/session`, POST `/login`, `/logout` | Sign-in |
| GET | `/system`, `/stats?minutes=60` | Host info, CPU and memory history |
| GET | `/containers`, `/containers/{id}` | List (with live CPU and memory), inspect |
| GET | `/containers/{id}/logs?tail=200&follow=1&timestamps=0` | Server-Sent Events, one JSON line per event |
| POST | `/containers/{id}/{start,stop,restart,pause,unpause,kill}` | Lifecycle |
| DELETE | `/containers/{id}?force=&volumes=` · POST `/containers/prune` | Remove |
| GET | `/images`, `/images/{id}` · POST `/images/pull` `{"ref"}` · DELETE `/images/{id}` · POST `/images/prune?all=` | Images |
| GET, POST | `/volumes` · DELETE `/volumes/{name}` · POST `/volumes/prune?all=` | Volumes |
| GET, POST | `/networks` · DELETE `/networks/{id}` · POST `/networks/{id}/{connect,disconnect}` · POST `/networks/prune` | Networks |
| GET | `/registry?refresh=` · DELETE `/registry/{repo}?tag=` | Registry |

## Layout

```
backend/
  cmd/server/          entry point and configuration
  internal/docker/     Engine API client over the Unix socket (+ log demultiplexing)
  internal/stats/      CPU/memory sampler with an hour of history
  internal/registry/   Registry API v2 client (catalog, tags, manifests, delete)
  internal/api/        HTTP handlers, auth, SSE log streaming
  internal/web/        embeds the built frontend
frontend/
  src/pages/           one file per tab
  src/components/      table, charts, log viewer, dialogs
  src/api.ts, types.ts typed client for the API
```

## Not yet

- Vulnerability scanning and image signatures for registry tags (planned via Trivy or Docker Scout).
- Several hosts, and TLS to remote daemons.
- Creating containers or compose stacks from the UI.
- Exec / terminal into a container.
