.PHONY: run test test-unit test-integration test-clean test-clean-run bench lint tidy build release \
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

# TEST_P caps how many package binaries `go test` runs at once. The default is
# the core count, which saturates an 8-core dev box: every integration package
# starts its own Postgres container under -race, and the contention made whole
# packages miss their container-start deadline (TASK-153). Override on a bigger
# CI runner with `make test-integration TEST_P=8`.
#
# test-unit is capped deliberately too, and not because of containers: 53
# packages under -race on the default -p is itself more load than this box is
# meant to carry while someone works on it. Do not "optimise" it back off.
TEST_P ?= 2

# TEST_TIMEOUT is the per-package budget. With one container per package plus
# template cloning the slowest package finishes well inside it; the limit is
# kept generous so that it only ever fires on a genuine hang.
TEST_TIMEOUT ?= 180s

# TEST_LABEL is stamped on every container started by internal/pkg/database/testdb
# (testdb.LabelKey/LabelValue). Deliberately := and not ?=: the label on the
# containers comes from a Go constant, so overriding this would only ever change
# what gets *deleted* — `make test-clean TEST_LABEL=org.testcontainers=true`
# would wipe every project's testcontainers on the host.
#
# TEST_RUN_ID is unique per make invocation and reaches the tests as
# SE_TEST_RUN_ID (testdb.RunIDEnv), which stamps it as a second label. The
# automatic cleanup after a run matches on that one, so two runs sharing a
# docker host cannot tear down each other's containers.
# TestUnit_TestDB_MakefileFiltersMatchLabels fails if these strings drift from
# the Go constants.
TEST_LABEL     := spaceempire.test=true
TEST_RUN_LABEL := spaceempire.test.run
TEST_RUN_ID    := $(shell date +%s%N)

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

# test and test-integration both run TestIntegration_ tests, so both carry the
# same three properties: -count=1 (a cached ok says nothing about whether the
# docker daemon still works — the suite once reported green in 21 s having run
# nothing), a per-package -timeout, and a cleanup pass afterwards that keeps the
# exit code of the test run itself. That last one matters because a binary
# killed by -timeout panics and skips every t.Cleanup and TestMain, which is how
# containers used to pile up.
test:
	@SE_TEST_RUN_ID=$(TEST_RUN_ID) TESTCONTAINERS_RYUK_DISABLED=$(RYUK_DISABLED) \
		go test -race -count=1 -p $(TEST_P) -timeout $(TEST_TIMEOUT) ./...; \
		status=$$?; $(MAKE) --no-print-directory test-clean-run; exit $$status

test-unit:
	go test -run '^TestUnit_' -race -p $(TEST_P) ./...

test-integration:
	@SE_TEST_RUN_ID=$(TEST_RUN_ID) TESTCONTAINERS_RYUK_DISABLED=$(RYUK_DISABLED) \
		go test -run '^TestIntegration_' -race -count=1 -p $(TEST_P) -timeout $(TEST_TIMEOUT) ./...; \
		status=$$?; $(MAKE) --no-print-directory test-clean-run; exit $$status

# test-clean-run reaps only this invocation's containers. It runs automatically
# after test / test-integration, where a blanket sweep would kill a concurrent
# run's live databases and bury it under connection errors.
test-clean-run:
	@ids=$$(docker ps -aq --filter "label=$(TEST_RUN_LABEL)=$(TEST_RUN_ID)"); \
	if [ -n "$$ids" ]; then echo "removing leaked test containers (run $(TEST_RUN_ID)):"; docker rm -f $$ids; fi

# test-clean sweeps every container this project's tests ever started, including
# those from runs that were killed outright, and from `go test` invoked directly
# (no run id). Manual by design — it is not safe to fire while another run is in
# flight. Matched strictly by TEST_LABEL, never by image name, so local developer
# databases are out of scope by construction.
test-clean:
	@ids=$$(docker ps -aq --filter "label=$(TEST_LABEL)"); \
	if [ -n "$$ids" ]; then echo "removing leaked test containers:"; docker rm -f $$ids; fi

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
