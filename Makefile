.PHONY: help up down logs logs-tail restart rebuild migrate migrate-down build run test clean doctor

# Default target
help:
	@echo "DriveBai Development Commands"
	@echo ""
	@echo "Infrastructure:"
	@echo "  make up          - Start PostgreSQL and API containers"
	@echo "  make up-db       - Start only PostgreSQL"
	@echo "  make down        - Stop all containers"
	@echo "  make restart     - Restart API container (reload .env changes)"
	@echo "  make rebuild     - Rebuild and restart API container"
	@echo "  make logs        - View API logs (follow mode)"
	@echo "  make logs-tail   - View last 100 lines of API logs"
	@echo "  make doctor      - Check prerequisites (Docker, etc.)"
	@echo ""
	@echo "Database:"
	@echo "  make migrate     - Run database migrations (via Docker)"
	@echo "  make migrate-down - Rollback last migration"
	@echo ""
	@echo "Development:"
	@echo "  make run         - Run API locally (requires Go + PostgreSQL)"
	@echo "  make build       - Build the Go binary (requires Go)"
	@echo "  make test        - Run tests (requires Go)"
	@echo "  make clean       - Clean build artifacts"
	@echo ""
	@echo "iOS:"
	@echo "  make ios-open    - Open iOS project in Xcode"

# Check prerequisites
doctor:
	@echo "Checking prerequisites..."
	@echo ""
	@command -v docker >/dev/null 2>&1 && echo "✅ Docker installed" || echo "❌ Docker not found - install from https://docker.com"
	@docker info >/dev/null 2>&1 && echo "✅ Docker daemon running" || echo "❌ Docker daemon not running - start Docker Desktop"
	@command -v docker-compose >/dev/null 2>&1 && echo "✅ docker-compose available" || (docker compose version >/dev/null 2>&1 && echo "✅ docker compose (v2) available" || echo "❌ docker-compose not found")
	@echo ""
	@echo "Optional (for local development without Docker):"
	@command -v go >/dev/null 2>&1 && echo "✅ Go installed: $$(go version)" || echo "⚠️  Go not installed (not required for 'make up')"
	@echo ""
	@echo "To start the project: make up"

# Start all services
up:
	@docker compose up -d --build
	@echo "Waiting for services to be ready..."
	@sleep 5
	@$(MAKE) migrate
	@echo ""
	@echo "✅ Services are running!"
	@echo "   API: http://localhost:8080"
	@echo "   Docs: http://localhost:8080/docs"
	@echo "   Health: http://localhost:8080/health"

# Start only database
up-db:
	@docker compose up -d postgres
	@echo "PostgreSQL is starting on port 5432..."

# Stop all services
down:
	@docker compose down

# View logs (follow mode)
logs:
	@docker compose logs -f api

# View last 100 lines of logs
logs-tail:
	@docker compose logs --tail=100 api

# Restart API container (picks up .env changes)
restart:
	@echo "Restarting API container..."
	@docker compose restart api
	@echo "✅ API restarted. View logs with: make logs"

# Rebuild and restart API container
rebuild:
	@echo "Rebuilding API container..."
	@docker compose up -d --build api
	@echo "✅ API rebuilt and restarted. View logs with: make logs"

# Run migrations via Docker (no local Go required)
migrate:
	@echo "Running migrations..."
	@docker run --rm --network host \
		-v "$(PWD)/backend/migrations:/migrations" \
		migrate/migrate:v4.17.0 \
		-path=/migrations \
		-database="postgres://drivebai:drivebai_secret@localhost:5432/drivebai?sslmode=disable" \
		up
	@echo "Migrations complete!"

# Rollback last migration via Docker
migrate-down:
	@echo "Rolling back migration..."
	@docker run --rm --network host \
		-v "$(PWD)/backend/migrations:/migrations" \
		migrate/migrate:v4.17.0 \
		-path=/migrations \
		-database="postgres://drivebai:drivebai_secret@localhost:5432/drivebai?sslmode=disable" \
		down 1

# Build the API (requires Go locally)
build:
	cd backend && go build -o bin/api ./cmd/api

# Run API locally (requires Go + PostgreSQL running)
run:
	cd backend && go run ./cmd/api

# Run tests (requires Go locally)
test:
	cd backend && go test -v ./...

# Clean build artifacts
clean:
	rm -rf backend/bin
	@docker compose down -v

# Open iOS project in Xcode
ios-open:
	open ios/DriveBai/DriveBai.xcodeproj
