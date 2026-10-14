# syntax=docker/dockerfile:1

# ---- frontend: React + TypeScript, built into the Go embed directory
FROM node:22-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN mkdir -p ../backend/internal/web/dist && npm run build

# ---- backend: one static binary with the frontend embedded
FROM golang:1.24-alpine AS api
WORKDIR /src/backend
COPY backend/go.mod ./
RUN go mod download
COPY backend/ ./
COPY --from=web /src/backend/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/docker-dashboard ./cmd/server

# ---- runtime: no shell, no package manager
FROM gcr.io/distroless/static-debian12
COPY --from=api /out/docker-dashboard /docker-dashboard
EXPOSE 8080
# The process needs access to the Docker socket. Mounting it as root is the
# simplest option; to run as a non-root user instead, add the socket's group
# (e.g. `group_add: ["999"]` in compose) and set `user: "65532"`.
ENTRYPOINT ["/docker-dashboard"]
