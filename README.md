# buildyard

A personal build manager for Maven projects: a local daemon with a web UI. Specification: [docs/PLAN.md](docs/PLAN.md); phase 0 spike results: [docs/SPIKE.md](docs/SPIKE.md).

## Build and run

Requires Go 1.26+.

```sh
make build              # bin/buildyard (no cgo needed)
make test
./bin/buildyard serve   # listens on 127.0.0.1:7777, open http://127.0.0.1:7777/
./bin/buildyard open    # open the UI in the browser
```

Data lives in `~/.buildyard` (`BUILDYARD_HOME` overrides it): `config.toml`, `buildyard.db`, `daemon.log`.

## Status

Phase 1 (skeleton): config, lock file, SQLite with migrations, projects CRUD and the projects screen.

Settings: a JDK list (auto-detected as a background task, plus manual entries), default executable and JDK; per project — `./mvnw`, mvnd, mvn or a custom path, a JDK and environment variables.
