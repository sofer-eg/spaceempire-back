.PHONY: run test test-unit test-integration bench lint tidy build release \
	db-up db-down db-psql migrate-up migrate-down migrate-status \
	tools

BINARY := bin/starwind

PG_USER     ?= enlarge_db
PG_PASSWORD ?= enlarge2501
PG_DB       ?= spaceempire
PG_HOST     ?= localhost
PG_PORT     ?= 5432
PG_DSN      ?= postgres://$(PG_USER):$(PG_PASSWORD)@$(PG_HOST):$(PG_PORT)/$(PG_DB)?sslmode=disable

MIGRATE     := go run ./cmd/migrate
MIGRATIONS  := ./migrations

# Ryuk (testcontainers reaper) is disabled by default: the dev box cannot pull
# testcontainers/ryuk from Docker Hub (registry-1.docker.io times out), while
# postgres:16-alpine is already cached locally. CI with registry access can
# override with `make test-integration RYUK_DISABLED=false`. See README.md
# "Integration tests" and docs/tasks/phase7-06-testcontainers-ryuk.md.
RYUK_DISABLED ?= true

run:
	go run ./cmd/starwind

build:
	mkdir -p bin
	go build -o $(BINARY) ./cmd/starwind

# release builds the production single binary: it compiles the React SPA,
# copies the bundle into internal/webui/dist (embedded via go:embed), then
# static-links the server (CGO off). The result bin/spaceempire is fully
# self-contained — it serves both the API/WS and the frontend. See
# deploy/README.md.
RELEASE_BINARY := bin/spaceempire
release:
	cd ../front && npm ci && npm run build
	rm -rf internal/webui/dist
	cp -r ../front/dist internal/webui/dist
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(RELEASE_BINARY) ./cmd/starwind
	@echo "built $(RELEASE_BINARY) (frontend embedded)"

test:
	TESTCONTAINERS_RYUK_DISABLED=$(RYUK_DISABLED) go test -race ./...

test-unit:
	go test -run '^TestUnit_' -race ./...

test-integration:
	TESTCONTAINERS_RYUK_DISABLED=$(RYUK_DISABLED) go test -run '^TestIntegration_' -race -timeout 180s ./...

bench:
	go test -run '^$$' -bench=. -benchmem ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

tools:
	@echo "Tooling pinned via go.mod. Migrations: ./cmd/migrate (uses embedded goose)."

db-up:
	docker run -d --name spaceempire-pg --rm \
		-e POSTGRES_USER=$(PG_USER) \
		-e POSTGRES_PASSWORD=$(PG_PASSWORD) \
		-e POSTGRES_DB=$(PG_DB) \
		-p $(PG_PORT):5432 \
		postgres:16-alpine
	@echo "Waiting for postgres to accept connections..."
	@until PGPASSWORD=$(PG_PASSWORD) psql -h $(PG_HOST) -p $(PG_PORT) -U $(PG_USER) -d $(PG_DB) -c 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
	@echo "Postgres ready at $(PG_DSN)"

db-down:
	docker stop spaceempire-pg || true

db-psql:
	PGPASSWORD=$(PG_PASSWORD) psql -h $(PG_HOST) -p $(PG_PORT) -U $(PG_USER) -d $(PG_DB)

migrate-up:
	PG_DSN="$(PG_DSN)" $(MIGRATE) up

migrate-down:
	PG_DSN="$(PG_DSN)" $(MIGRATE) down

migrate-status:
	PG_DSN="$(PG_DSN)" $(MIGRATE) status
