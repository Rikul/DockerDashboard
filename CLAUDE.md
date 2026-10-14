# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Requires Go 1.24+, Node 22+, and a Docker daemon (for running, not for tests).

```sh
make dev        # Go API on 127.0.0.1:8080 + Vite on :5173 (proxies /api), READ_ONLY=false. Sign in with "dev-token-change-me"
make test       # go vet + go test -race (backend) and tsc type-check (frontend)
make lint       # gofmt check (CI fails on unformatted Go)
make build      # builds frontend, then bin/docker-dashboard with it embedded
make image      # docker build -t docker-dashboard:local .

cd backend && go test -race ./internal/api -run TestName   # single Go test
cd frontend && npm run typecheck                            # no frontend test runner or linter exists
```

## Architecture

Single Go binary (standard library only, **no third-party Go modules** — keep it that way) serving a JSON API under `/api` and the embedded React SPA. One Docker host, configured via env vars (see README for the table).

- `backend/cmd/server/main.go` — config/env parsing and wiring.
- `backend/internal/docker` — hand-rolled Docker Engine API client over unix socket or TCP (`client.go`), log stream demuxing (`logs.go`), and the subset of API types used (`types.go`).
- `backend/internal/api` — `server.go` (Server struct, routing, middleware, caches for inspect/disk-usage, shutdown of log streams), `auth.go` (signed session cookie derived from the token, revocation list, per-client login throttling, CSRF header check), `handlers.go` (all endpoints).
- `backend/internal/stats` — background sampler keeping ~1h of CPU/memory history in memory.
- `backend/internal/registry` — Registry API v2 client (basic + bearer-token auth) for the Registry tab.
- `backend/internal/web` — `//go:embed all:dist`. If `dist/` has only `.gitkeep`, `web.FS()` returns nil and only the API is served.
- `frontend/` — React 19 + TypeScript + Vite, d3 for charts. `src/api.ts` is the single fetch wrapper (adds the required `X-Requested-With` header; dispatches an unauthorized event on 401); `src/types.ts` mirrors the backend JSON; `pages/` has one file per tab.

### Build coupling

Vite's `outDir` is `backend/internal/web/dist` (emptied on build, `.gitkeep` restored), so `npm run build` feeds the Go embed directly. Build the frontend before `go build` for a full binary; the `Dockerfile` does this in stages and ships a distroless image.

## Conventions worth knowing

- Every state-changing request must carry `X-Requested-With`; the frontend wrapper does this, so use it rather than raw `fetch`.
- Sign-in is always required (`DASHBOARD_TOKEN`, ≥16 chars); there is no auth-disabled mode.
- `READ_ONLY` defaults to `true` (the server refuses all writes unless it is set to `false`); new mutating endpoints must respect it.
- Container env vars must never be sent to the browser (stripped in the API layer).
- CI (`.github/workflows/ci.yml`) runs gofmt, `go vet`, `go test -race`, `npm ci` and `npm run build`.
