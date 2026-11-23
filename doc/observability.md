# Observability

Buildyard can send every build to an OpenTelemetry stack you run yourself, such as Grafana, Loki and Tempo. You can see where a build spends its time and compare the same build on two machines. Export is **off** by default. Logs and the web UI work the same with or without it.

## What is sent

| Signal | Backend | Content |
|---|---|---|
| Timeline (traces) | Tempo | One trace per run: `startup` (JVM, POMs, extensions), a span per module, and under it a span per goal and per download |
| Build output (logs) | Loki | The run's output, linked to its trace |

Spans and log records carry `host.name` and the project, task and tool (`mvn`, `mvnd`, `mvnw`). The root span also carries the exit code, the git commit and download totals (count, bytes, time). Download spans carry the repository, the size and whether the artifact was found. This works with mvn, mvnd and `./mvnw` through Buildyard's Maven extension.

Each output line is sent as it is, one log record per line, with the run's context: the trace, `buildyard.log.line` and, when modules build one at a time, `maven.module` and `maven.goal`. Buildyard doesn't parse the output: levels and stack traces are the observability stack's job, see [Parse Maven output](#parse-maven-output).

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
- Logs: Explore → Loki, `{service_name="buildyard"} | buildyard_project="my-app"`, or **Related logs** on a trace

## Parse Maven output

Put [Grafana Alloy](https://grafana.com/docs/alloy/) between Buildyard and the stack. It reads the level from each line (`[ERROR] msg`, `[main] [ERROR] msg` or `[my-module] [INFO] msg`) and stores it, with the run's context, as structured metadata in Loki. The line itself is kept as it is, minus colors. Each line is parsed on its own: lines are not merged into multi-line entries, because modules built in parallel interleave their output. Traces pass through unchanged. Save this as `config.alloy`:

```alloy
otelcol.receiver.otlp "buildyard" {
  http {
    endpoint = "0.0.0.0:4318"
  }

  output {
    traces = [otelcol.exporter.otlphttp.lgtm.input]
    logs   = [otelcol.exporter.loki.buildyard.input]
  }
}

otelcol.exporter.otlphttp "lgtm" {
  client {
    endpoint = "http://lgtm:4318"

    tls {
      insecure = true
    }
  }
}

otelcol.exporter.loki "buildyard" {
  forward_to = [loki.process.maven.receiver]
}

loki.process "maven" {
  stage.json {
    expressions = {
      body               = "body",
      trace_id           = "traceid",
      span_id            = "spanid",
      buildyard_project  = `attributes."buildyard.project"`,
      buildyard_task     = `attributes."buildyard.task"`,
      buildyard_job_id   = `attributes."buildyard.job.id"`,
      buildyard_log_line = `attributes."buildyard.log.line"`,
      maven_module       = `attributes."maven.module"`,
      maven_goal         = `attributes."maven.goal"`,
    }
  }

  stage.output {
    source = "body"
  }

  stage.decolorize {}

  // Parse each line independently; never merge parallel module output.
  stage.regex {
    expression = `^(?:\[(?P<maven_module_artifact>[^\]]+)\]\s+)?\[(?P<level>TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)\](?:\s+(?P<message>.*))?$`
  }

  stage.structured_metadata {
    values = {
      level                 = "",
      trace_id              = "",
      span_id               = "",
      buildyard_project     = "",
      buildyard_task        = "",
      buildyard_job_id      = "",
      buildyard_log_line    = "",
      maven_module          = "",
      maven_module_artifact = "",
      maven_goal            = "",
    }
  }

  forward_to = [loki.write.lgtm.receiver]
}

loki.write "lgtm" {
  endpoint {
    url = "http://lgtm:3100/loki/api/v1/push"
  }
}
```

The regex must stay on one line: a line break inside it is matched literally, and then no line matches.

And run it in front of otel-lgtm (`compose.yaml` next to it). Buildyard keeps sending to `http://localhost:4318`, which is Alloy now:

```yaml
services:
  lgtm:
    image: grafana/otel-lgtm
    ports: ["3000:3000"]                 # Grafana
    volumes: ["lgtm-data:/data"]
  alloy:
    image: grafana/alloy
    command: ["run", "/etc/alloy/config.alloy"]
    volumes: ["./config.alloy:/etc/alloy/config.alloy:ro"]
    ports: ["4318:4318"]                 # OTLP/HTTP from Buildyard
volumes:
  lgtm-data:
```

Then filter by level: `{service_name="buildyard"} | level="ERROR"`, or by module prefix: `| maven_module_artifact="my-module"`.

Without Alloy, parse at query time instead: `{service_name="buildyard"} | regexp "^\\[(?P<level>[A-Z]+)\\]" | level="ERROR"`.
