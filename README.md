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
(`TESTCONTAINERS_RYUK_DISABLED=true`) skips it. Containers are still torn down
by the test's own `t.Cleanup`/`Terminate`; Ryuk is only a backstop for crashed
runs.

### CI / hosts with registry access

Override the default to re-enable the reaper:

```bash
make test-integration RYUK_DISABLED=false
```

Alternatively, mirror `testcontainers/ryuk:0.13.0` into an internal registry
and point testcontainers at it with `TESTCONTAINERS_RYUK_CONTAINER_IMAGE`.

See `docs/tasks/phase7-06-testcontainers-ryuk.md` for the full rationale.
