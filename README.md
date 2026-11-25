# buildyard

A personal build manager with a web UI for Java projects.

Documentation: https://tools.binaryc.at/buildyard

## Quick installation

Run script in your shell

```sh
curl -fsSL https://raw.githubusercontent.com/binarycat0/buildyard/main/install.sh | sh
```

### Quick start

```sh
buildyard service install   # launchd agent (macOS) or systemd --user unit (Linux)
buildyard service start
buildyard open              # http://127.0.0.1:7777/
```

In the UI, **Add project** with the path to a project root, create a task with **New task** (modules, skips, arguments, resources) and press **Run** next to it.

## Install

The install script checks the release's SHA-256, installs the binary to `~/.buildyard/bin` and links it from `~/.local/bin` (no sudo). Run it again to upgrade; a running service is restarted. `BUILDYARD_VERSION=v0.1.0` pins a release, `BUILDYARD_BIN_DIR` changes where the link goes.

The service runs `buildyard serve --service` with the `PATH`, `JAVA_HOME` and `BUILDYARD_HOME` of the shell that ran `service install`, so it finds mvn, mvnd and java the way that shell does; run `service install` again after changing them. `buildyard service stop|restart|status|uninstall` manage it; stopping it interrupts running builds.

Data lives in `~/.buildyard` (`BUILDYARD_HOME` overrides it): `config.toml`, `buildyard.db` and `daemon.log` (the service's output). Settings → General edits `maxConcurrentJobs`, `retention.jobsPerProject` and, on macOS, `keepAwake` in `config.toml` and applies them at once (saving drops comments in the file); after editing the file by hand, `kill -HUP <pid>` reloads it. The port needs a restart.

To uninstall: `buildyard service uninstall`, then delete `~/.local/bin/buildyard` and `~/.buildyard`.

## Features

### Web UI

Projects, saved build tasks, live logs with a module tree and problems, run history and comparison, and settings for JDKs and build tool config files. See [Web UI](doc/ui.md).

### Observability (optional)

Builds can also be sent to an OpenTelemetry stack you run yourself (Grafana, Loki, Tempo): a trace per run with startup, modules, goals and downloads, and the build output as logs. See [Observability](doc/observability.md) for setup with Docker.

### MCP for agents

Coding agents connect to `http://localhost:7777/mcp` to add projects and tasks, run builds and read results. See [MCP for agents](doc/mcp.md).

### Keep the Mac awake (macOS)

An option in Settings → General runs `caffeinate` next to each build, so idle sleep can't interrupt it. See [Keep the Mac awake](doc/keep-awake.md).

## CLI

With the daemon running, the CLI drives it (see `buildyard help`):

```sh
buildyard status                 # daemon and projects with their last job
buildyard run "Quick build"      # run a saved task of the project you are in, follow its log
buildyard run -d -p my-app Full  # queue it and print the job id
buildyard jobs                   # recent jobs
buildyard logs -f 42             # follow a job's log
buildyard cancel 42
```

`run` exits non-zero unless the build succeeded, so it works in scripts and git hooks. Ctrl-C stops following; the build keeps running.

## Development

### Prerequisites

- Go 1.26+
- Maven, only for `make ext` (the extension jar is committed)
- For the docs site: Docker or Podman, or else Python 3 (a venv in `.venv-docs`)

### Build and run

```sh
make build    # bin/buildyard (no cgo needed)
make test     # go vet, go test -race
make install  # like the install script, from this checkout
make uninstall
```

Run a second instance next to the installed service, with its own data: `BUILDYARD_HOME=/tmp/by-test ./bin/buildyard serve --port 7790`.

### maven-ext

`make ext` rebuilds the Maven extension in `maven-ext/`. 

### Docs site

`make docs` builds the docs site into `site/` and `make docs-serve` previews it on http://127.0.0.1:8000; the home page is generated from this README.

### CI and releases

CI runs gofmt, vet and race tests on every pull request. To release, push a `v*` tag or run the **Release** workflow on `main` with a version such as `v0.1.0`; it builds darwin and linux binaries (amd64, arm64) with checksums. 
The docs site deploys when a push to `main` changes the README, `install.sh` or the docs setup.

## License

MIT, see [LICENSE](LICENSE).
