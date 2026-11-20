# buildyard

A personal build manager for Maven projects: a local daemon with a web UI.

Documentation: https://tools.binaryc.at/buildyard

## Quick installation

macOS or Linux, from a release (no Go needed):

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

Data lives in `~/.buildyard` (`BUILDYARD_HOME` overrides it): `config.toml`, `buildyard.db` and `daemon.log` (the service's output). Settings → General edits `maxConcurrentJobs` and `retention.jobsPerProject` in `config.toml` and applies them at once (saving drops comments in the file); after editing the file by hand, `kill -HUP <pid>` reloads it. The port needs a restart.

To uninstall: `buildyard service uninstall`, then delete `~/.local/bin/buildyard` and `~/.buildyard`.

## Observability (optional)

Buildyard can also send every build to an OpenTelemetry stack you run yourself, to see where the time goes (startup, each module, each plugin goal, artifact downloads) and to compare the same build on two machines. It is off by default; the local logs and the UI work the same either way.

- **Traces** (Tempo): one trace per run. `startup` covers the JVM, POM loading and extensions; then a span per module, with a span per goal (`maven-compiler-plugin:compile (default-compile)`) and per download (`download g:a:jar:v`, with repository, size and whether it was found). Spans carry `host.name`, the project, task and tool (mvn, mvnd, mvnw) and download totals. Works with mvn, mvnd and ./mvnw through Buildyard's Maven extension.
- **Logs** (Loki): every output line, under `service_name="buildyard"`, linked to the run's trace; the job id, project, task and line number are structured metadata.

Start Grafana, Loki and Tempo in one container ([grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm)):

```sh
docker run -d --name lgtm -p 3000:3000 -p 4318:4318 grafana/otel-lgtm
```

or with Compose (`docker compose up -d`), keeping data across restarts:

```yaml
services:
  lgtm:
    image: grafana/otel-lgtm
    ports: ["3000:3000", "4318:4318"]   # Grafana, OTLP/HTTP
    volumes: ["lgtm-data:/data"]
volumes:
  lgtm-data:
```

Then in Settings → Observability turn on **Export builds** (endpoint `http://localhost:4318`), press **Test** and **Save**. Runs started afterwards show a trace link on their page that opens Grafana Explore (the link assumes the Tempo data source uid `tempo`, as in otel-lgtm). In Grafana: Explore → Tempo for traces (TraceQL, e.g. `{resource.service.name="buildyard" && span.buildyard.project="my-app"}`), Explore → Loki for logs (`{service_name="buildyard"} | buildyard_project="my-app"`).

The same settings live in `config.toml`:

```toml
[telemetry]
enabled = true
endpoint = "http://localhost:4318"   # OTLP/HTTP; /v1/traces and /v1/logs are appended
traces = true
logs = true
serviceName = "buildyard"
grafanaUrl = "http://localhost:3000"
# [telemetry.headers]                # for a hosted backend
# Authorization = "Basic ..."
```

Any OTLP/HTTP receiver that accepts JSON works, e.g. Grafana Alloy or an OpenTelemetry Collector in front of your own Loki and Tempo. Export never slows a build down: when the collector is down, records are dropped and counted on the Settings page.

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
- Docker or Podman, only for the docs site

### Build and run

```sh
make build    # bin/buildyard (no cgo needed)
make test     # go vet, go test -race
make install  # like the install script, from this checkout
make uninstall
```

Run a second instance next to the installed service, with its own data: `BUILDYARD_HOME=/tmp/by-test ./bin/buildyard serve --port 7790`.

`make ext` rebuilds the Maven extension in `maven-ext/`. `make docs` builds the docs site into `site/` and `make docs-serve` previews it on http://127.0.0.1:8000; the home page is generated from this README.

### CI and releases

CI runs gofmt, vet and race tests on every pull request. To release, push a `v*` tag or run the **Release** workflow on `main` with a version such as `v0.1.0`; it builds darwin and linux binaries (amd64, arm64) with checksums. The docs site deploys when a push to `main` changes the README, `install.sh` or the docs setup.

## License

MIT, see [LICENSE](LICENSE).
