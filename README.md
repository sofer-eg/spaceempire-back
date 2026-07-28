# spaceempire — backend

Go server for Space Empire (rewrite of StarWind). Modular monolith,
sector-per-worker with state in RAM, Postgres for persistence, HTTP + WebSocket
API. See the design doc and `CLAUDE.md` at the repo root for architecture.

## Layout

- `cmd/starwind` — server entrypoint
- `cmd/migrate` — goose migration runner (embedded migrations)
- `cmd/starwind-tools` — one-off tools (e.g. balance converter)
- `internal/` — domain, sector workers, persistence, API, economy/social modules
- `migrations/` — goose SQL migrations (embedded)
- `docs/specs/` — per-feature ports of the old StarWind stored procedures

## Common tasks

```bash
make run                 # run the server
make build               # build bin/starwind
make lint                # golangci-lint
make test-unit           # unit tests (-race), TestUnit_*
make test-integration    # integration tests (-race), TestIntegration_*
make test-clean          # reap every test container this project left behind
make migrate-up          # apply migrations to PG_DSN
make migrate-status      # show migration state
```

Postgres connection is configured via the `PG_*` Make variables (default
`localhost:5432`, db `spaceempire`). Override `PG_DSN` to point elsewhere.

## Integration tests

Integration tests (`TestIntegration_*`) use
[`testcontainers-go`](https://golang.testcontainers.org/) to spin up an
ephemeral `postgres:16-alpine`. By default they run with the **Ryuk reaper
disabled**:

```bash
make test-integration            # TESTCONTAINERS_RYUK_DISABLED=true (default)
```

### How the fixtures work

`internal/pkg/database/testdb` starts **one container per test binary** (i.e.
per package), lazily on the first `testdb.Setup` call. The goose migrations run
once, into a template database; each `Setup` then clones it with
`CREATE DATABASE ... TEMPLATE`, which Postgres serves as a file copy — tens of
milliseconds, against ~500ms to replay the migrations and ~2s to start a
container. Every test still gets its own database, so isolation and
`t.Parallel()` are unchanged, and `Setup(t) *pgxpool.Pool` is unchanged for
callers.

Packages that call `testdb.Setup` must wire the shared container's teardown:

```go
func TestMain(m *testing.M) { testdb.Main(m) }
```

`Setup` fails the test with that snippet if the package has not declared it —
otherwise the omission would be invisible (the tests still pass) and the package
would leak its container on every run.

`make test-integration` also caps how many package binaries run at once
(`TEST_P`, default 2). The core-count default saturated an 8-core box — every
integration package racing to start its own Postgres under `-race`, with whole
packages then missing their container-start deadline. Raise it on a bigger
runner:

```bash
make test-integration TEST_P=8
```

The per-package budget is `TEST_TIMEOUT` (default 180s), comfortably above the
slowest package, so it only fires on a genuine hang. The target also passes
`-count=1`: these tests depend on state Go does not key its build cache on (the
docker daemon, the image cache, container startup), so a cached `ok` can report
green for an environment that no longer works.

### Leaked containers

A binary killed by `go test -timeout` panics and skips every `t.Cleanup` **and**
`TestMain`, so its container survives the run. `make test` and
`make test-integration` therefore always finish with a cleanup pass, keeping the
test run's own exit code.

That pass removes **only the containers of the invocation that is finishing**.
Each `make` invocation generates a `TEST_RUN_ID` and passes it to the tests as
`SE_TEST_RUN_ID` (`testdb.RunIDEnv`), which `testdb` stamps as a second label.
Without that scoping, two runs sharing a docker host would destroy each other:
the first to finish would `docker rm -f` the other's live databases. To reap a
past run by hand: `make test-clean-run TEST_RUN_ID=<id>`.

Both sweeps go through `scripts/reap-test-containers.sh`. When the test run
failed, it keeps sweeping until two consecutive passes come up empty: a binary
killed while testcontainers was mid-create leaves the daemon to finish the job,
so containers keep appearing (in state `created`) for seconds after `go test`
has returned. A single immediate sweep measured 11 of them left behind.

For a total sweep — containers from runs that were killed outright, or from
`go test` invoked directly, which carry no run id — there is a manual target:

```bash
make test-clean
```

It is manual by design: it is not safe to fire while another run is in flight.

A container from a `go test` run that bypassed make carries no run id, so only
this manual sweep can reap it — by construction there is no invocation for an
automatic pass to belong to.

Both match strictly on labels `testdb` stamps on the containers it starts
(`testdb.LabelKey`/`LabelValue`, `testdb.RunLabelKey`) — never on the image name.
The script refuses any other filter, so scope is decided where `docker rm -f`
actually runs rather than by its caller. The Makefile's own wiring is checked
against those Go constants by `TestUnit_TestDB_MakefileReapsWhatItStamps`,
because drift here does not fail anything: the filters would simply stop
matching and the leaks would come back silently.

### Why Ryuk is disabled

Testcontainers normally starts a sidecar `testcontainers/ryuk` container that
cleans up leftover containers after the run. On the current dev box `docker
pull` from Docker Hub (`registry-1.docker.io`) times out, so the reaper
bootstrap fails with:

```
reaper: new reaper: run container: Error response from daemon:
Get "https://registry-1.docker.io/v2/": EOF
```

`postgres:16-alpine` is already cached locally, so the actual test container
starts fine — only the reaper bootstrap fails. Disabling Ryuk
(`TESTCONTAINERS_RYUK_DISABLED=true`) skips it. Containers are torn down by
`testdb.Main` at the end of each package, with `make test-clean` as the backstop
for runs that were killed outright (see above); Ryuk is not required either way.

### CI / hosts with registry access

Override the default to re-enable the reaper:

```bash
make test-integration RYUK_DISABLED=false
```

Alternatively, mirror `testcontainers/ryuk:0.13.0` into an internal registry
and point testcontainers at it with `TESTCONTAINERS_RYUK_CONTAINER_IMAGE`.

See `docs/tasks/phase7-06-testcontainers-ryuk.md` for the full rationale.

### Upsert schema guard

`internal/pkg/database/schemaguard` extracts every `INSERT ... ON CONFLICT`
literal from the sources (`go/ast`) and plans each one against the migrated
schema with `EXPLAIN (GENERIC_PLAN, COSTS OFF)`, which needs PostgreSQL 16+.
Planning executes nothing as long as the literal is a *single* statement:
`EXPLAIN` covers only the first statement of a string and the rest would run, so
literals carrying an inner `;` are rejected rather than planned, and the whole
check runs inside a rolled-back transaction.

It fails when an `ON CONFLICT` target no longer matches any UNIQUE/PK key
(42P10), when a target names a constraint the table no longer has (42704, the
`ON CONFLICT ON CONSTRAINT` form), or when the literal names a missing
column/table — naming the literal's `file:line` and listing the table's actual
keys. A literal that cannot be planned at all fails the test as well: an
unchecked target is exactly the hole the guard exists to close, so it is never a
silent skip. A migration that changes a UNIQUE/PRIMARY KEY must be paired with a
grep of `ON CONFLICT` for that table — see the root `CLAUDE.md`,
"Миграции и ON CONFLICT".
