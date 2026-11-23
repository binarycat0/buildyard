# Observability

Buildyard can send every build to an OpenTelemetry stack you run yourself, such as Grafana, Loki and Tempo. You can see where a build spends its time and compare the same build on two machines. Export is **off** by default. Logs and the web UI work the same with or without it.

## What is sent

| Signal | Backend | Content |
|---|---|---|
| Timeline (traces) | Tempo | One trace per run: `startup` (JVM, POMs, extensions), a span per module, and under it a span per goal and per download |
| Build log (logs) | Loki | The run's log records, linked to its trace |

Spans and log records carry `host.name` and the project, task and tool (`mvn`, `mvnd`, `mvnw`). The root span also carries the exit code, the git commit and download totals (count, bytes, time). Download spans carry the repository, the size and whether the artifact was found. This works with mvn, mvnd and `./mvnw` through Buildyard's Maven extension.

On [supported Maven versions](#structured-logs) each log call is sent as one record with its level, module, goal and stack trace, ready to filter in Loki with no extra parsing. On other versions each output line is sent as it is, one record per line, with the trace, `buildyard.log.line` and, when modules build one at a time, `maven.module` and `maven.goal`.

## Start a stack

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) runs Grafana, Loki, Tempo and an OTLP collector in one container. Publish the ports, or Buildyard can't reach it:

```sh
docker run -d --name lgtm -p 3000:3000 -p 4318:4318 grafana/otel-lgtm
```

To keep the data across restarts, use Docker Compose (`docker compose up -d`):

```yaml
services:
  lgtm:
    image: grafana/otel-lgtm
    ports: ["3000:3000", "4318:4318"]   # Grafana, OTLP/HTTP
    volumes: ["lgtm-data:/data"]
volumes:
  lgtm-data:
```

Grafana is at <http://localhost:3000> (admin / admin). otel-lgtm runs Grafana, Loki, Tempo and Prometheus in one container: give Docker at least 4 GB of memory, or Grafana can crash.

## Configure Buildyard

Go to **Settings → Observability**, turn on **Send builds to OpenTelemetry**, press **Test**, then **Save**. Runs that start after that are exported, and their page gets a trace link that opens Grafana. The link assumes the Tempo data source uid `tempo`, as in otel-lgtm.

The settings live in `~/.buildyard/config.toml`:

```toml
[telemetry]
enabled = true
endpoint = "http://localhost:4318"   # OTLP/HTTP (JSON); /v1/traces and /v1/logs are appended
traces = true
logs = true
serviceName = "buildyard"
grafanaUrl = "http://localhost:3000"
# [telemetry.headers]                # for a hosted backend
# Authorization = "Basic ..."
```

Export never slows a build down. When the collector is down, records are dropped, and the Settings page counts them.

## Find builds in Grafana

- Traces: Explore → Tempo, `{resource.service.name="buildyard" && span.buildyard.project="my-app"}`
- Logs: Explore → Loki, `{service_name="buildyard"} | buildyard_project="my-app"`, or **Related logs** on a trace. A run's page links to its records (**logs in Grafana**; it assumes the Loki data source uid `loki`, as in otel-lgtm).

## Structured logs

On the versions below, Buildyard runs Maven with its own logger wrapped around Maven's. The console output and the log file stay exactly the same. In addition, every log call is written as one JSON line to the run's **JSON log** (linked on the run's page), and that is what goes to Loki. Nothing to install or configure: no Alloy, no change to the project or the Maven installation.

| Runtime | Tested versions |
|---|---|
| Maven 3 (`mvn`, `./mvnw`, a custom path) | 3.9.6, 3.9.9, 3.9.10, 3.9.11 |
| Maven 4 | 4.0.0-rc-7 |
| mvnd | 1.0.2, 1.0.6 |

Other versions run exactly as before, with raw lines. `./mvnw` gets it once its Maven is downloaded (from its second run on).

How it gets in: Maven's launchers load `${maven.conf}/logging` before their own jars. Buildyard makes a copy of the installation's `conf` folder under `~/.buildyard/ext/logging`, with links to the original files and the logger's classes in `logging/`, and passes `-Dmaven.conf=<copy>` in `MAVEN_OPTS`. mvnd ignores that setting, so for mvnd Buildyard runs the client from a stand-in installation in the same folder (links to the real one, a generated `mvn/conf`). Its daemons are separate from the ones your own `mvnd` uses.

Each record carries:

| Attribute (Loki name) | Content |
|---|---|
| severity (`severity_text`, `detected_level`) | `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR` |
| `maven.module` (`maven_module`) | `group:artifact` of the module the log call was made for, also with `-T` and mvnd |
| `maven.goal` (`maven_goal`) | `compiler:compile (default-compile)` |
| `logger.name`, `thread.name` | The logger, or `stdout`/`stderr` for what plugins print directly (such as test output) |
| `exception.type`, `exception.message`, `exception.stacktrace` | When the call logged an exception: the whole stack trace stays in one record |

A message that spans several lines, such as a failed test with its stack trace, is one record.

Examples:

```logql
{service_name="buildyard"} | detected_level="error"
{service_name="buildyard"} | buildyard_project="my-app" | maven_module="com.example:core"
{service_name="buildyard"} | maven_goal=~"surefire:test.*" | detected_level="error"
{service_name="buildyard"} | exception_type!=""
```
