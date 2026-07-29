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

# TEST_LABEL / TEST_RUN_LABEL are the labels internal/pkg/database/testdb stamps
# on every container it starts (testdb.LabelKey/LabelValue, testdb.RunLabelKey).
# `override` because they must not be settable from the command line: the labels
# on the containers come from Go constants, so an override changes nothing about
# what is created and everything about what is deleted —
# `make test-clean TEST_LABEL=org.testcontainers=true` would wipe every
# project's testcontainers on this host. Note that := alone does NOT prevent
# this; only override does.
override TEST_LABEL     := spaceempire.test=true
override TEST_RUN_LABEL := spaceempire.test.run

# TEST_RUN_ID is unique per make invocation and reaches the tests as
# SE_TEST_RUN_ID (testdb.RunIDEnv), which stamps it as a second label; the
# cleanup after a run matches on that one, so two runs sharing a docker host
# cannot tear down each other's containers. Overridable on purpose, to reap a
# specific past run by hand.
#
# %N is a GNU date extension — on BSD/macOS it prints literally and the id
# degrades to one-per-second. Harmless: colliding ids only take cleanup back to
# the old behaviour of sweeping a concurrent run.
#
# The id is per make *invocation*, not per target, so `make -jN test
# test-integration` gives both targets the same one and whichever finishes first
# reaps the other's containers. Running them as separate invocations (the usual
# way, and what CI does) is unaffected.
TEST_RUN_ID := $(shell date +%s%N)

# REAP is the cleanup both sweeps go through; it takes a label filter and the
# exit status of the test run (see the script for why the status matters).
#
# Called directly rather than through a `$(MAKE) test-clean-run` sub-make: a
# sub-make re-parses this file and re-evaluates TEST_RUN_ID, so the child
# filtered on an id nothing had ever been stamped with and quietly matched
# nothing. Calling the script keeps one id per invocation, and keeps `make -n`
# from executing the recipe (make runs lines containing $(MAKE) even under -n,
# `go test ./...` and all), which is what lets
# TestUnit_TestDB_MakefileReapsWhatItStamps check this wiring.
REAP := scripts/reap-test-containers.sh

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
		status=$$?; $(REAP) "$(TEST_RUN_LABEL)=$(TEST_RUN_ID)" $$status || echo "warning: container cleanup did not finish; run 'make test-clean'" >&2; exit $$status

# test-unit carries -count=1 for a reason that has nothing to do with docker: a
# cached ok says nothing about whether the tests still pass. This project checks
# its tests by mutation — break the code, the test must fail — and the cache
# quietly answers with the old green whenever the edit missed the packages under
# test, so a caught defect reads as uncaught. It hid flakes too, which is how the
# omission was found (TASK-158, TASK-164).
#
# -timeout is the same per-package budget as above, for a different failure: unit
# packages finish in seconds, so it only ever fires on a hang, and a deadlock
# under -race then costs 180s instead of go test's 10-minute default.
#
# The flag list is spelled out in each recipe rather than shared through one
# variable: -count=1 being visible at the point of use is half of what makes it
# stick, and the guard against the recipes drifting apart is
# TestUnit_TestDB_MakefileTestTargetsDefeatTheCache — which finds the targets
# running go test and checks each invocation, so a new target or a second
# invocation is covered too — not DRY.
test-unit:
	go test -run '^TestUnit_' -race -count=1 -p $(TEST_P) -timeout $(TEST_TIMEOUT) ./...

test-integration:
	@SE_TEST_RUN_ID=$(TEST_RUN_ID) TESTCONTAINERS_RYUK_DISABLED=$(RYUK_DISABLED) \
		go test -run '^TestIntegration_' -race -count=1 -p $(TEST_P) -timeout $(TEST_TIMEOUT) ./...; \
		status=$$?; $(REAP) "$(TEST_RUN_LABEL)=$(TEST_RUN_ID)" $$status || echo "warning: container cleanup did not finish; run 'make test-clean'" >&2; exit $$status

# test-clean-run reaps one run's containers: the pass that test / test-integration
# run inline, exposed as a target so a past run can be reaped by hand with
# `make test-clean-run TEST_RUN_ID=<id>`. A blanket sweep here would kill a
# concurrent run's live databases and bury it under connection errors.
test-clean-run:
	@$(REAP) "$(TEST_RUN_LABEL)=$(TEST_RUN_ID)"

# test-clean sweeps every container this project's tests ever started, including
# those from runs that were killed outright, and from `go test` invoked directly
# (no run id). Manual by design — it is not safe to fire while another run is in
# flight. Matched strictly by TEST_LABEL, never by image name, so local developer
# databases are out of scope by construction.
test-clean:
	@$(REAP) "$(TEST_LABEL)"

# No -count=1 here: -bench is not one of the flags go keys its test cache on, so
# a run with it is never served from the cache in the first place. No -timeout
# either, which is not the same as no limit — go test's own 10-minute default
# still applies, and a benchmark that needs longer has to raise it itself. What
# benchmarks stay out of is TEST_TIMEOUT, which is sized for tests.
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
