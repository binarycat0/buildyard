# Observability

Buildyard can send every build to an OpenTelemetry stack you run yourself, such as Grafana, Loki and Tempo. You can see where a build spends its time and compare the same build on two machines. Export is off by default. Logs and the web UI work the same with or without it.

## What is sent

| Signal | Backend | Content |
|---|---|---|
| Timeline (traces) | Tempo | One trace per run: `startup` (JVM, POMs, extensions), a span per module, and under it a span per goal and per download |
| Build output (logs) | Loki | The run's output, linked to its trace |

Spans and log records carry `host.name` and the project, task and tool (`mvn`, `mvnd`, `mvnw`). The root span also carries the exit code, the git commit and download totals (count, bytes, time). Download spans carry the repository, the size and whether the artifact was found. This works with mvn, mvnd and `./mvnw` through Buildyard's Maven extension.

With **Parse Maven output** on (the default), log records are shaped this way:

- `[ERROR] msg` becomes severity `ERROR` with the message `msg`. The same applies to `WARNING`, `INFO` and `DEBUG`.
- A stack trace (`\tat …`, `Caused by: …`, `... 12 more`) becomes one record together with the line it belongs to.
- Records get `maven.module` and `maven.goal` from Maven's `--- plugin:goal (id) @ module ---` markers, when modules build one at a time.

Other output, such as forked tests or scripts, stays one record per line. With the option off, every line is sent as it is.

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

Grafana is at <http://localhost:3000> (admin / admin).

## Configure Buildyard

Go to **Settings → Observability**, turn on **Send builds to OpenTelemetry**, press **Test**, then **Save**. Runs that start after that are exported, and their page gets a trace link that opens Grafana. The link assumes the Tempo data source uid `tempo`, as in otel-lgtm.

The settings live in `~/.buildyard/config.toml`:

```toml
[telemetry]
enabled = true
endpoint = "http://localhost:4318"   # OTLP/HTTP (JSON); /v1/traces and /v1/logs are appended
traces = true
logs = true
parseLogs = true
serviceName = "buildyard"
grafanaUrl = "http://localhost:3000"
# [telemetry.headers]                # for a hosted backend
# Authorization = "Basic ..."
```

Export never slows a build down. When the collector is down, records are dropped, and the Settings page counts them.

## Find builds in Grafana

- Traces: Explore → Tempo, `{resource.service.name="buildyard" && span.buildyard.project="my-app"}`
- Logs: Explore → Loki, `{service_name="buildyard"} | buildyard_project="my-app"`, or **Related logs** on a trace

## Parsing in a collector instead

Turn **Parse Maven output** off and send to [Grafana Alloy](https://grafana.com/docs/alloy/) (or an OpenTelemetry Collector with the same `transform` statements). Alloy then sets the severity in front of your backend:

```alloy
otelcol.receiver.otlp "buildyard" {
  http { endpoint = "0.0.0.0:4318" }
  output {
    logs   = [otelcol.processor.transform.maven.input]
    traces = [otelcol.exporter.otlphttp.backend.input]
  }
}

otelcol.processor.transform "maven" {
  error_mode = "ignore"
  log_statements {
    context    = "log"
    statements = [
      `set(severity_text, "ERROR") where IsMatch(body, "^\\[ERROR\\] ")`,
      `set(severity_number, 17) where IsMatch(body, "^\\[ERROR\\] ")`,
      `set(severity_text, "WARN") where IsMatch(body, "^\\[WARNING\\] ")`,
      `set(severity_number, 13) where IsMatch(body, "^\\[WARNING\\] ")`,
      `replace_pattern(body, "^\\[(ERROR|WARNING|INFO|DEBUG)\\] ", "")`,
    ]
  }
  output { logs = [otelcol.exporter.otlphttp.backend.input] }
}

otelcol.exporter.otlphttp "backend" {
  client { endpoint = "http://lgtm:4318" }   # your Loki/Tempo OTLP endpoint
}
```

Collectors only join multi-line stack traces when they read log files themselves, not for logs that arrive over OTLP. That is why Buildyard does the grouping itself.
