# buildyard

A personal build manager for Maven projects: a local daemon with a web UI. Specification: [docs/PLAN.md](docs/PLAN.md); phase 0 spike results: [docs/SPIKE.md](docs/SPIKE.md).

## Install

macOS or Linux, from a release (no Go needed):

```sh
curl -fsSL https://raw.githubusercontent.com/binarycat0/buildyard/main/install.sh | sh
buildyard service install   # launchd agent (macOS) or systemd --user unit (Linux)
buildyard service start     # then open http://127.0.0.1:7777/
```

The script checks the release's SHA-256, installs the binary to `~/.buildyard/bin` and links it from `~/.local/bin` (no sudo). Run it again to upgrade; a running service is restarted. `BUILDYARD_VERSION=v0.1.0` pins a release, `BUILDYARD_BIN_DIR` changes where the link goes. From a checkout, `make install` does the same with a local build.

The service runs `buildyard serve --service` with the `PATH`, `JAVA_HOME` and `BUILDYARD_HOME` of the shell that ran `service install`, so it finds mvn, mvnd and java the way that shell does; run `service install` again after changing them. Its output goes to `~/.buildyard/daemon.log`. `buildyard service stop|restart|status|uninstall` manage it; stopping it interrupts running builds.

To uninstall: `make uninstall`, or `buildyard service uninstall` and then delete `~/.local/bin/buildyard` and `~/.buildyard`.

## Build and run

Requires Go 1.26+.

```sh
make build              # bin/buildyard (no cgo needed)
make test               # go vet, go test -race
./bin/buildyard serve   # listens on 127.0.0.1:7777, open http://127.0.0.1:7777/
./bin/buildyard open    # open the UI in the browser
```

A second instance for development: `BUILDYARD_HOME=/tmp/by-test ./bin/buildyard serve --port 7790`.

Releases: push a `v*` tag, or run the **Release** workflow on `main` with a version such as `v0.1.0`. It builds darwin and linux binaries (amd64, arm64) with checksums. CI runs gofmt, vet and race tests on every pull request.

Data lives in `~/.buildyard` (`BUILDYARD_HOME` overrides it): `config.toml`, `buildyard.db`, `daemon.log`.

## Status

Phase 1 (skeleton): config, lock file, SQLite with migrations, projects CRUD and the projects screen.

Settings: a JDK list (auto-detected as a background task, plus manual entries), default executable and JDK; per project — `./mvnw`, mvnd, mvn or a custom path, a JDK and environment variables.
