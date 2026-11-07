# Two processes in development: the Go backend owns the hardware, Vite serves
# the UI and proxies /api and /ws to it.

GO_DIR  := go
WEB_DIR := gorc
ADDR    ?= :8080

.PHONY: help dev-api dev-web build test lint run clean

help:
	@echo "make dev-api   # backend on $(ADDR), reachable at http://localhost:8080"
	@echo "make dev-web   # Vite dev server on http://localhost:5173 (proxies to the backend)"
	@echo "make test      # Go unit + integration tests"
	@echo "make lint      # oxlint + go vet"
	@echo "make build     # production frontend bundle + backend binary"
	@echo "make run       # single process: backend serving the built frontend"

dev-api:
	cd $(GO_DIR) && go run ./cmd/connector -addr $(ADDR)

dev-web:
	cd $(WEB_DIR) && npm run dev

test:
	cd $(GO_DIR) && go test ./...

lint:
	cd $(GO_DIR) && go vet ./...
	cd $(WEB_DIR) && npm run lint

build:
	cd $(WEB_DIR) && npm run build
	cd $(GO_DIR) && go build -o bin/connector ./cmd/connector

run: build
	cd $(GO_DIR) && ./bin/connector -addr $(ADDR) -static ../$(WEB_DIR)/dist

clean:
	rm -rf $(GO_DIR)/bin $(WEB_DIR)/dist
