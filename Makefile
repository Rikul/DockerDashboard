.PHONY: dev dev-api dev-web build web test lint image clean

BIN := bin/docker-dashboard

# Run the API (with a throwaway token) and the Vite dev server side by side.
# Open http://localhost:5173 and sign in with "dev-token-change-me".
dev:
	@$(MAKE) -j2 dev-api dev-web

dev-api:
	cd backend && DASHBOARD_TOKEN=dev-token-change-me go run ./cmd/server

dev-web: frontend/node_modules
	cd frontend && npm run dev

frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci
	@touch $@

# Build the frontend, then one binary with it embedded.
build: web
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../$(BIN) ./cmd/server
	@echo "built $(BIN)"

web: frontend/node_modules
	cd frontend && npm run build

test:
	cd backend && go vet ./... && go test -race ./...
	cd frontend && npm run typecheck

lint:
	cd backend && test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

image:
	docker build -t docker-dashboard:local .

clean:
	rm -rf bin frontend/dist
	find backend/internal/web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
