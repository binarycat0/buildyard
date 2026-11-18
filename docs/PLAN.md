# Buildyard — plan and architecture

Nov 17, 2026 · @binarycat

A personal build manager for Java projects: a daemon plus a web UI. This document is the specification for implementation sessions. Source document: https://claude.ai/artifact/RXSQR1bTaMc5MuMefepcdB

## How to use this document

This is the full specification. A new session starts with phase 0 and moves on to the next phase only after the current phase's done criteria are met.

1. Export the document to Markdown and put it in the repository as `docs/PLAN.md`.
2. Open a new session in the repository and give it the starter prompt below.
3. After each phase the session reports briefly: what was done and where decisions diverged from the plan. Divergences are written back into this document.

```text
Read docs/PLAN.md — it is the specification for Buildyard, a personal build manager
for Java projects (daemon + web UI). Start with phase 0 (spike). Do not start phase N+1
until the done criteria of phase N are met. Treat the decisions in "Key decisions" as
accepted; if you want to deviate, ask first. Write tests for the command builder and
the parsers from day one. Everything in the repository (code, UI, docs, commits) is in English.
```

## Goal and scope

Buildyard takes over the routine around Maven in large multi-module projects. Dremio is the nearest example, not the target: Buildyard stays generic for any Maven project, and project-specific flags live in project settings and presets, not in code. What it gives: background builds, choosing modules and flags in the UI, saved run profiles, readable logs, and history with per-module timings.

There is one user — a developer on their own machine (macOS or Linux). The daemon listens on localhost only.

**Success criterion:** the typical loop "changed code in two modules → rebuild only what is needed, without tests" takes 2 clicks and needs no memorised flags.

Non-goals:

- Not a CI server: no agents, distributed builds, users or roles.
- Not a build system: Buildyard composes the Maven command and watches it; it compiles nothing itself.
- Not an IDE: no editor, code navigation or debugger.
- No custom build cache: mvnd and maven-build-cache-extension exist for that, and Buildyard can use them.

## Functional requirements

The MVP is phases 1–4: projects, runs with chosen modules and flags, profiles, background jobs and readable logs. The package scanner and "build changed" come right after. Anything not in the table goes to the ideas list, not into code.

| ID | Requirement | Phase |
| --- | --- | --- |
| F1 | Add a project: root path and name; the build system is detected automatically | 1 |
| F2 | The build system is set per project: Maven now, Gradle later through the same interface | 3 (Maven), 7 (Gradle) |
| F3 | Run builds, tests and arbitrary project commands (for example, starting a server) | 2 |
| F4 | Module selection: tree with checkboxes, search, `-am` / `-amd` | 3 |
| F5 | Flags and presets: skipping tests and checks, offline, `-T`, `-P` profiles, `-D` properties, choice of mvn / mvnd / mvnw | 3 |
| F6 | Preview of the final command line with a copy button | 3 |
| F7 | Run profiles: save, clone, pin, run in 1 click | 3 |
| F8 | Maven log level and log filter in the UI | 3 (flag), 4 (filter) |
| F9 | Background jobs: queue, cancel, keep running with the browser closed | 2 |
| F10 | Run history with logs and rerun | 2 |
| F11 | Per-module timings, n/m progress, comparing two runs, problems panel | 4 |
| F12 | Package scanner: per-module tree and a combined tree | 5 |
| F13 | "Build changed" from git diff | 6 |
| F14 | Completion notifications, daemon autostart (done), CLI client (done), old log cleanup | 7 |
| F15 | Global settings: list of available JDKs (auto-detected + manual) and per-build-tool defaults (Maven: executable and JDK); per-project JDK, build-tool overrides (executable, JDK for the tool) and environment | 1 (done) |

## Key decisions

Stack: Go, a single binary with an embedded UI and SQLite without cgo. The only Java part is a small Maven extension for structured events, because Maven only loads Java classes. Its jar is embedded into the binary with `go:embed`.

| Question | Decision | Why |
| --- | --- | --- |
| Daemon language | Go (current stable) | One static binary, fast start, little memory; goroutines suit reading processes and SSE |
| HTTP | `net/http` with standard pattern routing (`GET /api/jobs/{id}`); SSE by hand via `http.Flusher` | No framework: the stdlib is enough |
| Storage | SQLite via `modernc.org/sqlite` (pure Go, no cgo) + `database/sql`; migrations with `pressly/goose` and embedded SQL files | Cross-compilation without a C toolchain |
| Job logs | Files on disk, only metadata in the DB | A large project's log easily reaches tens of MB |
| Daemon logs | `log/slog` to `~/.buildyard/daemon.log` | Stdlib |
| UI | Static HTML + Alpine.js + ES modules, embedded with `go:embed`; JSON API + `EventSource`. UI text is English | No node build; the CLI uses the same API later |
| Module discovery | From Maven itself: `help:active-profiles` (offline, with the project's flags) lists every module and its active profiles without running lifecycle plugins (5.5 s for 952 modules in the spike). Runs as a background task. No hand-written pom walker | Exact reactor, fast enough; decided by the user after the spike |
| Build events | Own Java Maven extension (EventSpy), loaded with `-Dmaven.ext.class.path`, writes JSON Lines. The jar is embedded in the binary and unpacked to `~/.buildyard/ext/` on start | Exact start and end of modules and mojos regardless of log format; the project is not changed. Log parsing stays as a fallback |
| Build executable | Mode `auto | wrapper | mvnd | mvn | /absolute/path`. The global default is in the Maven settings; a project can override it. `auto`: the project's `./mvnw` if present, else mvnd, else mvn. mvn/mvnd are looked up on PATH and in Homebrew/sdkman dirs | The wrapper pins the project's Maven version; mvnd gives a warm JVM |
| JDK | A JDK list in global settings. Detection asks managers first (macOS `/usr/libexec/java_home -X`, `brew --prefix`, `SDKMAN_DIR`/`JENV_ROOT`/`ASDF_DATA_DIR`/`MISE_DATA_DIR`), then scans well-known paths (JavaVirtualMachines, Homebrew opt/ and Cellar/, sdkman, jenv, asdf, mise, Gradle jdks, /usr/lib/jvm, JAVA_HOME); plus manual entries. Version/vendor/arch come from the `release` file, falling back to `java -XshowSettings:properties -version`. Each JDK gets a name from its install location (`openjdk@21 21.0.8 (Homebrew)`, `21.0.7-tem (sdkman)`, `temurin-21`) and keeps the path it was found at. Like IntelliJ, each build tool has its own JDK setting. The JDK a Maven run uses: the project's "JDK for Maven" → the project JDK → the global Maven default → the daemon's `JAVA_HOME`. Bazel/Gradle will get the same pair of settings | Different projects need different JDKs, and every Java-based tool has its own JDK setting |
| Background tasks | JDK detection, module and package scans are queued in `internal/tasks`: the API returns the task at once (`202`), the UI polls `GET /api/tasks/{id}`. One active task per key | Long operations never hold an HTTP request |
| Stopping processes | `os/exec` + `SysProcAttr{Setpgid: true}`; cancel = SIGTERM (SIGINT for the mvnd client) to the whole group, SIGKILL after 10 s | Works on macOS and Linux, kills surefire, node and other children |
| Command | Always a `[]string` argv, never a shell | No quoting or injection problems |
| CLI and config | Subcommands on stdlib `flag` (`buildyard serve`, `service`, and CLI client commands such as `run`, `jobs`, `logs`); config in `~/.buildyard/config.toml` (`BurntSushi/toml`) for daemon settings; UI-editable settings in the DB | Minimal dependencies |
| Building Buildyard | `Makefile`: first the extension jar (Maven, `--release 11`), then `go build`; releases by `.github/workflows/release.yml` (darwin/linux, amd64/arm64, checksums) and `install.sh` | JDK and Maven are on the machine anyway |
| Security | None beyond binding to `127.0.0.1`: no token, login or Host/Origin checks (user decision, 2026-11-17) | Personal tool on the user's own machine |

## Architecture

One Go daemon process sits between the browser and Maven. It composes the command, keeps the queue, writes the log and collects events from the Maven extension. The build itself stays an ordinary Maven process.

_The architecture diagram ("Buildyard architecture · daemon, 3 services, Maven adapter") is only in the source document: https://claude.ai/artifact/RXSQR1bTaMc5MuMefepcdB_

The runner is at the centre: every build goes through it. Project analysis only reads files and runs nothing; the exception is module discovery through Maven, which runs as a background task.

Repository layout:

```text
buildyard/
  cmd/buildyard/main.go     # serve, open, service; CLI client: status, run, jobs, logs, cancel
  internal/
    config/                # config.toml, ~/.buildyard paths
    lock/                  # flock on daemon.lock
    store/                 # SQLite, goose migrations (migrations/*.sql via embed)
    project/               # project root validation, build tool detection
    jdk/                   # JDK detection
    tasks/                 # background task queue
    buildtool/             # BuildTool interface, RunSpec, SkipPreset
    buildtool/maven/       # executable resolution, command builder, OutputParser
    runner/                # job queue, locks, processes, logs, events, bus
    scan/                  # package scanner
    changes/               # git diff -> modules
    httpapi/               # routes, SSE
    ext/                   # extension jar via go:embed + unpacking
  web/                     # static UI (Alpine.js) + embed.go
  maven-ext/               # Java: pom.xml + EventSpy
  testdata/                # log and event fixtures, sample-maven/
  docs/PLAN.md, docs/SPIKE.md
  Makefile
```

`go:embed` cannot see files above the package directory. So `make` first builds `maven-ext/` and copies the jar into `internal/ext/`, and only then runs `go build`. The jar is in `.gitignore`.

## Data model and RunSpec

Everything revolves around one object, RunSpec — the description of a run. A profile is a saved RunSpec; a job stores a snapshot of it at launch time. Data lives in `~/.buildyard/buildyard.db`, logs in `~/.buildyard/logs/`, the package index in `~/.buildyard/cache/<project>/packages.json`.

```sql
CREATE TABLE project (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL,
  root_path         TEXT NOT NULL UNIQUE,
  build_tool        TEXT NOT NULL DEFAULT 'maven', -- maven | gradle (later)
  executable        TEXT,                          -- NULL = global default; auto | wrapper | mvnd | mvn | /abs/path
  jdk_id            INTEGER REFERENCES jdk(id) ON DELETE SET NULL, -- project JDK; NULL = not set
  tool_jdk_id       INTEGER REFERENCES jdk(id) ON DELETE SET NULL, -- JDK for the build tool; NULL = project JDK
  env_json          TEXT NOT NULL DEFAULT '{}',
  skip_presets_json TEXT NOT NULL DEFAULT '[]',    -- project's own skip presets
  created_at        TEXT NOT NULL
);

CREATE TABLE jdk (
  id          INTEGER PRIMARY KEY,
  home        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL DEFAULT '',     -- user label
  version     TEXT NOT NULL DEFAULT '',     -- from the release file
  major       INTEGER NOT NULL DEFAULT 0,
  vendor      TEXT NOT NULL DEFAULT '',
  arch        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL,                -- manual | macos | homebrew | sdkman | jenv | ...
  missing     INTEGER NOT NULL DEFAULT 0,   -- not found by the last detection
  detected_at TEXT NOT NULL
);

CREATE TABLE setting (                      -- global settings edited in the UI
  key   TEXT PRIMARY KEY,                   -- tool.<id>, e.g. tool.maven = {executable, jdkId}; detected.*
  value TEXT NOT NULL                       -- JSON
);

CREATE TABLE module (
  id              INTEGER PRIMARY KEY,
  project_id      INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  group_id        TEXT NOT NULL,
  artifact_id     TEXT NOT NULL,
  rel_path        TEXT NOT NULL,             -- module dir relative to the root
  packaging       TEXT NOT NULL,
  parent_rel_path TEXT,                      -- for the aggregator tree
  profiles_json   TEXT NOT NULL DEFAULT '[]',-- if the module is only added by a profile
  deps_json       TEXT NOT NULL DEFAULT '[]',-- dependencies inside the reactor
  source          TEXT NOT NULL,             -- static | maven
  discovered_at   TEXT NOT NULL,
  UNIQUE (project_id, group_id, artifact_id)
);

CREATE TABLE run_profile (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  spec_json  TEXT NOT NULL,                  -- RunSpec
  pinned     INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE (project_id, name)
);

CREATE TABLE job (
  id           INTEGER PRIMARY KEY,
  project_id   INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  profile_id   INTEGER REFERENCES run_profile(id) ON DELETE SET NULL,
  spec_json    TEXT NOT NULL,                -- RunSpec snapshot
  argv_json    TEXT NOT NULL,                -- final command
  status       TEXT NOT NULL,                -- queued|running|succeeded|failed|cancelled|interrupted
  exit_code    INTEGER,
  queued_at    TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT,
  git_head     TEXT,
  log_path     TEXT NOT NULL,
  log_lines    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE job_module (
  job_id      INTEGER NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  module_key  TEXT NOT NULL,                 -- groupId:artifactId
  ord         INTEGER NOT NULL,              -- reactor order
  status      TEXT NOT NULL,                 -- pending|running|succeeded|failed|skipped
  started_at  TEXT,
  duration_ms INTEGER,
  PRIMARY KEY (job_id, module_key)
);

CREATE TABLE job_problem (
  id         INTEGER PRIMARY KEY,
  job_id     INTEGER NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  module_key TEXT,
  kind       TEXT NOT NULL,                  -- compile_error | test_failure | plugin_error
  file       TEXT,
  line       INTEGER,
  message    TEXT NOT NULL,
  log_line   INTEGER                         -- to jump to the log line
);

CREATE TABLE shell_command (                 -- arbitrary project commands
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  argv_json  TEXT NOT NULL,
  cwd        TEXT NOT NULL DEFAULT '.',
  UNIQUE (project_id, name)
);
```

Example Maven RunSpec:

```json
{
  "kind": "maven",
  "goals": ["install"],
  "modules": {
    "mode": "selected",
    "list": ["com.dremio:dremio-common", "com.dremio:dremio-services-base"],
    "alsoMake": true,
    "alsoMakeDependents": false
  },
  "profiles": [],
  "skips": ["tests", "checkstyle", "javadoc"],
  "properties": {},
  "flags": {
    "offline": true,
    "noSnapshotUpdates": true,
    "threads": "1C",
    "noTransferProgress": true,
    "failMode": "fast"
  },
  "logLevel": "default",
  "executable": null,
  "env": {},
  "extraArgs": []
}
```

`modules.mode` is `all`, `selected` or `changed`. In `changed` mode the list is computed at launch and stored in the job snapshot. An arbitrary command runs as `{"kind": "shell", "commandId": 3}` and refers to a saved `shell_command`. Module names in the example are illustrative.

## BuildTool and the Maven adapter

Everything that depends on the build system sits behind one interface. Only Maven is implemented in the first version, but Gradle must be addable without changes to the runner, API or UI.

```go
type BuildTool interface {
    ID() string                                   // "maven"
    Detect(root string) bool                      // has pom.xml
    DiscoverCommand(p Project, profiles []string) []string // argv for module discovery
    ParseDiscovery(output []byte) ([]Module, error)
    BuildCommand(p Project, spec RunSpec, eventsFile string) ([]string, error)
    NewOutputParser() OutputParser                // log lines -> level, problems, module boundaries
    DefaultSkipPresets() []SkipPreset
}
```

### Module discovery

Modules come from Maven, run as a background task with the project's executable, JDK, environment and the profiles/skip flags the user selects:

1. `<executable> -B -o help:active-profiles <flags>` — lists every reactor module (`Active Profiles for Project 'g:a:packaging:version'`) with its active profiles, without running lifecycle plugins. If it fails offline (the help plugin is not in the local repository), it is retried once online.
2. The same run loads the Buildyard extension (`-Dmaven.ext.class.path`, `-Dbuildyard.events`). Its `session` event gives each module's folder, its aggregator (the project whose `<modules>` list it) and its direct in-reactor dependencies. The UI shows the aggregator tree (ticking a parent ticks its subtree), "depends on" / "used by" per module, and how many modules `-am` / `-amd` add. Without the event (the extension did not load) the list stays flat.
3. Results are stored in `module` and refreshed on demand and when any `pom.xml` changes.

There is no hand-written pom.xml walker: the user decided the Maven listing is enough (2026-11-17).

### Building the command

The argv order is fixed so the preview and tests stay stable:

1. Executable: `spec.executable` → `project.executable` → global Maven default → `auto` (`./mvnw` → mvnd → mvn).
2. Always `-B`: no colours or prompts; the line level comes from the `[INFO]` / `[WARNING]` / `[ERROR]` prefix.
3. Flags: `-o`, `-nsu`, `-T <n>`, `-ntp`, `-ff` / `-fae`, `-e`.
4. Log level: `quiet` → `-q`, `debug` → `-X`, `default` → nothing.
5. Modules: `-pl :a,:b` plus `-am` and/or `-amd`; nothing for `mode = all`.
6. Profiles: `-P a,!b`.
7. Skips from presets (table below) and `-Dkey=value` properties.
8. Extension: `-Dmaven.ext.class.path=<jar> -Dbuildyard.events=<file>`.
9. Goals (`clean install` etc.), then `extraArgs` as is.

Default skip presets. Projects can edit them and add their own in project settings (for example, Dremio's `-Ddremio.no-ui` and `-Ddremio.no-native=true`):

| Preset | Arguments | Note |
| --- | --- | --- |
| tests | `-DskipTests` | Tests are compiled but not run |
| testCompile | `-Dmaven.test.skip=true` | Breaks modules that depend on a `test-jar`; the UI warns |
| checkstyle | `-Dcheckstyle.skip` |  |
| spotless | `-Dspotless.check.skip -Dspotless.apply.skip` |  |
| javadoc | `-Dmaven.javadoc.skip` |  |
| sources | `-Dmaven.source.skip` |  |
| enforcer | `-Denforcer.skip` |  |
| license | `-Dlicense.skip -Drat.skip` |  |

### Maven extension `buildyard-maven-ext`

- A Java class extending `AbstractEventSpy`, annotated `@Named @Singleton`, registered through `META-INF/sisu/javax.inject.Named`. Depends only on `maven-core` 3.9.x in `provided` scope; JSON is written by hand. Compiled with `--release 8` so it loads on whatever JDK the project builds on.
- Sources in `maven-ext/`; `make ext` rebuilds the jar into `internal/ext/`, where the daemon embeds it. The jar is committed (8 KB, reproducible), so `go build` does not need Maven. At startup the daemon writes it to `~/.buildyard/ext/buildyard-maven-ext-<hash>.jar`.
- Listens to `ExecutionEvent`: SessionStarted, ProjectStarted, ProjectSucceeded, ProjectFailed, ProjectSkipped, MojoStarted, MojoSucceeded, MojoFailed, SessionEnded.
- Writes JSON Lines to the file from `-Dbuildyard.events`, flushing after every line. Without that property it does nothing.

```text
{"t":"session","ts":1763400000000,"root":"/repo","projects":[{"key":"g:a","name":"A","dir":"/repo/a","packaging":"jar","aggregator":"g:root","parent":"g:root","deps":["g:b"]}]}
{"t":"project_start","key":"g:a","ts":1763400001000}
{"t":"mojo_start","key":"g:a","mojo":"maven-compiler-plugin:compile","ts":1763400001200}
{"t":"project_end","key":"g:a","status":"succeeded","ms":4210,"ts":1763400005210}
{"t":"session_end","ts":1763400090000,"status":"failed"}
```

**Fallback.** If there are no events (the extension did not load), `OutputParser` reconstructs module boundaries from `Building <name> ... [n/m]` lines and the `Reactor Summary` block. With `-T`, `-Dorg.slf4j.simpleLogger.showThreadName=true` adds a `[mvn-builder-<artifactId>]` prefix to every module line (see SPIKE.md).

## Running jobs

At most one job per project runs at a time; the rest wait in the queue. Two builds of the same repository break each other's `target/`. The global `maxConcurrentJobs` limit defaults to 1 because all projects write to a shared local repository.

Lifecycle: `queued → running → succeeded | failed | cancelled`. If the daemon crashed or was restarted, jobs in `queued` and `running` become `interrupted` on start. We do not reattach to processes; the UI offers a rerun.

Starting a job:

1. `BuildCommand` builds the argv. The job records the argv, the RunSpec snapshot and `git rev-parse HEAD`.
2. `exec.Cmd`: `Dir` = project root; `Env` = daemon environment + `project.env` + `JAVA_HOME` of the resolved JDK + `spec.env`; `Stdout` and `Stderr` share one pipe; `SysProcAttr{Setpgid: true}`.
3. A reader goroutine goes line by line (`bufio.Scanner` with a 1 MB buffer for long lines) and does three things: appends the line to `logs/<job>.log` (line number = its index), runs it through `OutputParser`, and publishes it on the job bus.
4. A second goroutine tails `events/<job>.jsonl`, updates `job_module` and publishes `module` events.
5. After `cmd.Wait()` the exit code, status and line count are written and the job releases the project lock.

Controls for a running job: **Stop** sends `SIGINT` to the process group, like Ctrl+C in a terminal. Maven runs its shutdown hooks and the build ends by itself; nothing is killed automatically. **Kill** sends `SIGKILL` to the group at once. After the main process exits, anything left in its group is killed. `cancel` (used on daemon shutdown) is `SIGTERM`, then `SIGKILL` after 10 seconds. For a queued job, any of them just removes it from the queue. For mvnd, `SIGINT` goes to the client first. The build itself runs in the long-lived mvnd daemon outside our group, so that daemon must not be killed. Whether SIGINT to the client really cancels the build is still to be verified (mvnd was not available in the spike).

Job bus and SSE:

- Each running job has a set of subscribers with buffered channels. A slow subscriber whose channel overflows is dropped; the browser reconnects by itself.
- On connect with `Last-Event-ID: N`, lines from the file starting at N+1 are sent first, then the live stream. Without the header, the last 2000 lines; the UI loads earlier ones in batches via `GET /log`.
- For long logs we keep an offset index: the byte position of every 1000th line, so the file is not read from the start.

Cleanup: keep the last 200 jobs per project and at most 2 GB of logs in total. Both limits are set in the config.

## Logs and log levels

Maven always writes the full log to the file; the "level" is mainly a UI filter. Context is not lost when a build fails. `-ntp` is added by default: it removes the noisiest output, download progress.

| `logLevel` | Arguments | When |
| --- | --- | --- |
| default | — | Normally; the UI filters |
| quiet | `-q` | When the log is not needed at all; little context on failure |
| debug | `-X` | Plugin diagnostics |
| custom | `-Dorg.slf4j.simpleLogger.log.<logger>=warn` | Quiet one noisy logger; works on Maven 3.9.16 (verified in the spike) |

What the UI log viewer does:

- ERROR / WARN / INFO / DEBUG level chips; a line's level comes from its prefix. Lines without a prefix (stack traces, test output) inherit the previous line's level.
- The "Important only" preset is on by default: ERROR, WARN, module boundaries, test summaries (`Tests run:`), `Reactor Summary`, `BUILD SUCCESS` / `BUILD FAILURE`.
- Plugin headers `--- plugin:goal (id) @ module ---` collapse their output into one line that expands on click.
- Search, auto-scroll with a toggle, a "first error" button.
- A problems panel from `job_problem`: compile errors like `[ERROR] /path/File.java:[12,5] message`, failed tests from the `Failed tests:` / `Tests in error:` block, plugin errors `Failed to execute goal`. A build failure is decided from the Reactor Summary and `Failed to execute goal`, not from any `[ERROR]` line (see SPIKE.md: false `[ERROR]`s exist).
- File paths are clickable and open in the IDE through the IntelliJ URL scheme. Which scheme works on the machine is checked in phase 7.
- ANSI sequences are stripped just in case, even with `-B`.

## Package scanner

The scanner builds a package tree from sources without running Maven and shows it in two views: per module and combined for the whole project. A scan is a background task.

1. Each module's directories: `src/main/java`, `src/test/java`, `src/main/kotlin`, `src/test/kotlin`. Generated code from `target/generated-sources/*` and `target/generated-test-sources/*` is marked generated and hidden by default.
2. In each `.java` / `.kt` file, read from the start up to the `package x.y.z` line (regex `^\s*package\s+([\w.]+)`, at most 64 KB). If there is no such line, the package comes from the directory path.
3. The walk is parallel: `filepath.WalkDir` and a worker pool sized to the CPU count.
4. The tree is a trie over package segments. A node stores the full name, class count, modules and kind (main / test / generated). Single-child chains collapse (`com.example.exec` as one node), like IntelliJ's compact middle packages.
5. Packages split across several modules (split packages) are highlighted in the combined view.
6. Cache in `cache/<project>/packages.json` together with file mtimes. A rescan rereads only changed files.
7. The API returns the tree lazily (children of a node), not as one giant JSON.

Clicking a package shows its files and a "build modules with this package" button that fills the module selection in the run builder. Target times for a large project like Dremio: a full scan under 10 seconds, a rescan under 2 seconds; verified in phase 5.

## "Build changed"

`modules.mode = changed` maps changed files to modules and puts them into `-pl`. For large projects this is probably the most useful feature of the whole tool.

1. The file list is the union of:
   - `git diff --name-only <base>...HEAD` — committed changes relative to the base;
   - `git diff --name-only` and `git diff --name-only --cached` — uncommitted changes;
   - `git ls-files --others --exclude-standard` — new files.
2. The base is chosen in the UI from three options:
   - **since the last successful run of this profile** — default; uses `job.git_head`;
   - from the merge-base with the upstream branch (or `origin/main`);
   - uncommitted changes only.
3. A file belongs to the module with the longest matching `rel_path` prefix. Files outside modules are ignored, except the root `pom.xml` and `.mvn/*`: changing those turns into "build everything" with a warning.
4. A changed module `pom.xml` includes the module itself and suggests enabling `-amd`.
5. Before the run, the UI shows the resulting module list and lets the user edit it.

The `-am` and `-amd` toggles stay with the user. Without `-am` the build assumes dependencies are already in the local repository from a previous `install`. `-amd` is needed to run the tests of dependent modules.

## HTTP API and events

All functionality is available through the JSON API under `/api`. The UI and the CLI (`internal/client`) use the same API. Errors come back as `{"error": "<code>", "message": "..."}` with the matching HTTP status. Long operations return `202` with a background task.

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/api/projects` | Projects with their last job |
| POST | `/api/projects` | Add `{name, rootPath}`: detect the build system |
| GET / PATCH / DELETE | `/api/projects/{id}` | A project and its settings (`executable`, `jdkId`, `toolJdkId`, `env`, `skipPresets`) |
| GET | `/api/projects/{id}/runtime` | Which executable and JDK a run will use and why; whether `./mvnw` exists |
| GET | `/api/projects/{id}/modules` | Modules in reactor order (name, packaging, active profiles), the project's skip presets merged with the built-in ones, and the built-in ones |
| POST | `/api/projects/{id}/modules/refresh` | Rediscover modules with `{spec}`'s profiles, skips and properties → `202 {task}` |
| GET | `/api/projects/{id}/packages?node=&module=` | Children of a package tree node |
| POST | `/api/projects/{id}/packages/rescan` | Rescan packages (background task) |
| GET | `/api/projects/{id}/changes?base=` | Changed files and modules |
| GET / POST | `/api/projects/{id}/profiles` | The project's run profiles ("tasks" in the UI) `{name, spec, pinned}`, with their last job |
| PUT / DELETE | `/api/profiles/{id}` | Update or delete a profile |
| GET / POST | `/api/projects/{id}/commands` | Arbitrary project commands |
| POST | `/api/projects/{id}/preview` | `{spec}` → `{argv, dir, javaHome, env, error}` without running |
| GET | `/api/projects/{id}/jobs?limit=` | The project's job history, newest first |
| POST | `/api/projects/{id}/jobs` | Run: `{spec}` or `{profileId}` → `201 job` |
| GET | `/api/jobs/{id}` | A job (with `liveLines` while running); later its modules and problems |
| POST | `/api/jobs/{id}/stop` | Graceful stop (`SIGINT`, like Ctrl+C); the job shows `stopRequestedAt` until it ends |
| POST | `/api/jobs/{id}/kill` | Kill the process group at once (`SIGKILL`) |
| POST | `/api/jobs/{id}/cancel` | `SIGTERM`, then `SIGKILL` after 10 s |
| POST | `/api/jobs/{id}/rerun` | Rerun with the same RunSpec snapshot |
| GET | `/api/jobs/{id}/modules` | Per-module progress: live for an active job, from `job_module` otherwise |
| GET | `/api/jobs/{id}/log?from=&limit=` | A batch of log lines `{from, lines, total}` (`levels=` later) |
| GET | `/api/jobs/{id}/stream?from=` | SSE: log, status (modules later) |
| GET | `/api/events` | SSE: status of all jobs for the global job bar |
| GET / PUT | `/api/settings` | Global settings: `{tools: {maven: {executable, jdkId}}}` |
| GET | `/api/tools` | mvn and mvnd with versions (from the last detection), project wrappers and their Maven versions, the daemon's `JAVA_HOME` |
| POST | `/api/tools/detect` | Detect mvn/mvnd and their versions as a background task → `202 {task}` |
| GET | `/api/jdk-managers` | JDK managers (macOS registry, Homebrew, SDKMAN!, jenv, asdf, mise, Gradle toolchains), whether installed, and how many JDKs came from each |
| GET / POST | `/api/jdks` | JDK list; add one manually `{home, name}` |
| POST | `/api/jdks/detect` | Detect JDKs as a background task → `202 {task}` |
| PATCH / DELETE | `/api/jdks/{id}` | Rename or remove a JDK |
| GET | `/api/tasks`, `/api/tasks/{id}` | Background tasks and their status |
| GET | `/api/health` | Daemon version and state |

Events in `/api/jobs/{id}/stream` (`event:` → `data:`):

- `log` — `{n, text}` (`level` later); the event `id:` is the line number `n`, which enables catch-up via `Last-Event-ID`. Without it (or `?from=N`) the stream starts with the last 2000 lines; the UI loads earlier ones through `/log`;
- `status` — `{status}` whenever it changes;
- `modules` — the whole module list `[{key, name, ord, status, startedAt, ms}]`, sent after the catch-up and again when the reactor becomes known; statuses are `pending | running | succeeded | failed | skipped | cancelled`;
- `module` — one changed module, same shape;
- `problems` — the problems found so far, after the catch-up; `problem` — one new problem `{kind, module, file, line, message, logLine}`;
- `end` — the finished job; the last event. The client closes the stream after it (a reconnect would only get `end` again).

Every 15 seconds the server sends a `: ping` comment so the connection does not time out.

## UI: screens

The UI has a job bar at the top, visible everywhere, and these screens. The main screen is the run builder: the exact final command is visible before pressing Run.

- **Job bar** — running and queued jobs with n/m progress. A click opens the job screen. Data comes from `/api/events`.
- **Projects** — cards: name, path, status and duration of the last job, pinned profiles as buttons. "Add project": a path field, checking that there is a pom.xml.
- **Project** — tabs:
  - *Run.* Module tree with checkboxes on the left, search and a "select changed" button. On the right: goals (clean / compile / install / verify / test chips and custom), skip preset checkboxes, flags, `-P` profiles, a `-D` property table, log level, executable. At the bottom: the command preview with a copy button, Run, and "Save as profile".
  - *Profiles.* A list with Run / Edit / Clone / Pin.
  - *History.* A job table: status, profile, modules, duration, commit, time. Selecting two jobs compares per-module timings.
  - *Packages.* A tree with a "per module / combined" switch, search and a generated toggle.
  - *Settings* (done). A "what a run will use" summary and tabs: *General* (name, environment variables), *JDK* (project JDK), *Maven* (executable: global default, auto, `./mvnw`, mvnd, mvn, custom path; JDK for Maven, defaulting to the project JDK). A future build tool gets its own tab. Later: skip preset editor, arbitrary commands.
- **Settings** (done) — Maven defaults (executable, JDK for Maven); build tools (mvn, mvnd with versions read from their install files, project wrappers with the Maven version they pin); JDKs grouped by the manager they came from, with "Detect again", manual add, rename and remove, and the managers not found on the machine.
- **Activity** (done 2026-11-18) — background tasks (JDK and build tool detection, module discovery) with status, duration, a short result and the first error line (full output on click). The nav link gets a red dot for a failure not seen yet and a blue one while something runs.
- **Job** — header (status, command, time, Cancel / Rerun), n/m progress bar, horizontal per-module timing bars (current module highlighted), log viewer and problems panel.

The log is rendered as a virtual list: only visible lines are in the DOM, otherwise the browser chokes on a large log. Hotkeys: `r` — rerun the current job, `/` — search the log, `e` — next error.

## Daemon, configuration, security

The daemon listens on `127.0.0.1` only. There is no token, login or Host / Origin check: the user decided this on 2026-11-17, since it is a personal tool on their own machine. Known trade-off: a web page in the browser can send requests to `127.0.0.1:7777`.

**Start.** `buildyard serve` listens on `127.0.0.1:7777` (the port is set in the config). A second instance does not start: `~/.buildyard/daemon.lock` holds the PID under `flock`. On start the daemon detects JDKs in the background and unpacks the embedded extension jar to `~/.buildyard/ext/buildyard-maven-ext-<version>.jar` if it is not there.

Data directory (`BUILDYARD_HOME` overrides it):

```text
~/.buildyard/
  config.toml
  daemon.lock
  daemon.log
  buildyard.db
  ext/buildyard-maven-ext-<version>.jar
  logs/<job>.log
  events/<job>.jsonl
  cache/<project>/packages.json
```

```toml
port = 7777
maxConcurrentJobs = 1

[retention]
jobsPerProject = 200
maxLogBytes = "2GB"

[defaults]
flags = ["-ntp"]
```

**Changing config without a restart** (done 2026-11-18). Settings → Daemon edits `maxConcurrentJobs` and `retention.jobsPerProject` through `GET/PUT /api/config`; they are written to `config.toml` (as a generic table, so unknown keys survive; comments do not) and applied to the runner at once. SIGHUP reloads the file. `port` needs a restart. `retention.maxLogBytes` and `defaults.flags` are not used by the code yet.

**Security:**

- Bind to `127.0.0.1` only; listening on `0.0.0.0` is not possible even through the config.
- No CORS.
- Arbitrary commands run only by the id of a saved `shell_command`, never as raw text in a run request. `extraArgs` in a RunSpec are passed to Maven as arguments, without a shell.

**Autostart** (pulled forward from phase 7, done 2026-11-18). `buildyard service install` writes a launchd plist to `~/Library/LaunchAgents/` on macOS or a systemd user unit to `~/.config/systemd/user/` on Linux. `service start|stop|restart|status|uninstall` manage it; the service runs `serve --service`, which logs to stderr only, and the service manager appends that to `daemon.log`. The service gets `PATH`, `USER`, `JAVA_HOME` and `BUILDYARD_HOME` from the shell that installed it; otherwise the daemon inherits the service environment, not the shell's. That is why mvn/mvnd are also looked up in Homebrew/sdkman dirs and JDKs are chosen from the settings list rather than taken from `JAVA_HOME`.

## Phases

Eight phases in total; the MVP ends with phase 4. Each phase ends with a working binary and a verifiable done criterion. The next phase does not start until the criterion is met.

0. **Spike** (code is thrown away, answers stay) — **done 2026-11-17**, see `docs/SPIKE.md`. Closed without a fully green Dremio build, by the user's decision.
   - Hook a minimal EventSpy via `-Dmaven.ext.class.path` into a Dremio build with mvn and with mvnd. Does it work with mvnd? Is a new jar version picked up without restarting the mvnd daemon? *(not done: mvnd not installed)*
   - Measure: number of Dremio modules, module discovery time, which profiles add modules. *(done)*
   - Check: `-Dorg.slf4j.simpleLogger.log.*` on Dremio's Maven version *(works)*; mvnd output with `-B` *(not done)*; whether SIGINT to the mvnd client cancels the build *(not done)*.
   - Find Dremio's properties for skipping UI and the distribution. *(done)*
   - Collect log and event fixtures: successful build, compile error, failed tests, cancel. *(log fixtures done; events and cancel pending)*
   - **Done when:** answers are in `docs/SPIKE.md`, fixtures are in `testdata/`, and divergences from this plan are written into it.
1. **Skeleton** — **done**
   - Repository layout and `Makefile`, config, lock file, `slog`.
   - SQLite and goose migrations.
   - Projects CRUD, embedded UI with the projects screen.
   - Global settings (JDK list, per-build-tool defaults), project settings (project JDK, Maven executable and JDK, environment), background task queue.
   - **Done when:** `buildyard serve` starts the daemon; a project can be added and survives a restart; a second instance does not start.
2. **Job runner** — **done 2026-11-18**
   - Queue and per-project lock, log to file. Divergence: instead of saved shell commands the runner starts with a minimal Maven RunSpec (`goals` + free-form `args`), so real builds can run before phase 3; arbitrary shell commands move to phase 7.
   - SSE with `Last-Event-ID` catch-up, process group cancel, `interrupted` status after restart.
   - Job screen with a plain log, history. The project screen gets Run / History / Settings tabs.
   - Checked by `internal/runner` and `internal/httpapi` tests: cancel kills a parent and children that ignore SIGTERM; a subscriber that joins mid-run gets every line exactly once; two jobs of one project run one after another while another project runs in parallel.
   - **Done when:** a test script that spawns a child process is cancelled with no processes left (automated test); a browser opened mid-job receives the whole log; two jobs of one project run sequentially.
3. **Maven adapter and profiles** — **done 2026-11-18** (checked on `testdata/sample-maven` with real Maven; the Dremio check is left to the user)
   - Module discovery through Maven (background task), module tree with checkboxes and a filter (the tree came with the extension, see phase 4).
   - RunSpec → argv, preview, skip presets (editable per project, stored as changes to the built-in ones), run profiles, log level.
   - The UI calls run profiles **tasks**, because "profile" already means a Maven `-P` profile. Pinned tasks come first. `spec.executable` and `spec.env` overrides are not in the form yet.
   - **Done when:** on a large project (e.g. Dremio) you can pick 2 modules + `-am` + skip tests, see the command, run it, save it as a profile and rerun it in 1 click; table tests for the command builder are green.
   - **Resources** — **done 2026-11-18** (user request): CPU (`-T`), Maven memory and test JVMs as simple choices instead of JVM flags. `RunSpec.resources {heapMb, testForks}` plus `flags.threads`; project defaults in `project.resources_json {threads, heapMb, testForks}` (Maven tab), applied to empty run values. Heap goes into `MAVEN_OPTS` as `-Xmx` (replacing one already there; for mvnd `-Dmvnd.maxHeapSize`), test JVMs into `-DforkCount` unless the run sets that property itself. Explicit "Maven's own behaviour" values: threads `1`, heap `-1`, forks `none`. Test JVM memory is not offered: it would mean overriding `argLine`, which projects often set themselves.
4. **Events, timings, logs** (end of MVP)
   - The production extension and its embedding — **done 2026-11-18**, pulled forward for the module tree.
   - `job_module`, n/m progress, modules being built with elapsed time, failed modules, a module table sorted by build order or duration — **done 2026-11-18**. The runner adds the extension to every Maven job (`-Dmaven.ext.class.path`, `-Dbuildyard.events=logs/<id>.events.jsonl`, left out of the recorded argv), tails the events file every 250 ms, and saves the result to `job_module` when the job ends; modules still running then become `failed` (or `cancelled` after Stop/Kill). Works at any log level, including `-q`.
   - Level filter and "Important only" — **done 2026-11-18** (client-side classification; `Warnings and errors` log level).
   - Problems panel, comparing two runs, fallback parser — **done 2026-11-18**. `maven.LogParser` reads every output line (the runner calls it through `Command.Parser`): compile errors (`/path/File.java:[l,c] msg`, deduplicated: javac errors are printed twice), failed tests from the Surefire `Failures:` / `Errors:` (and old `Failed tests:` / `Tests in error:`) blocks, `Failed to execute goal … on project X` (kind `goal`, or `dependency` without a goal), with the module from the thread name or the last `--- … @ module ---`. Bare `[ERROR]` lines are not problems (fixture 06). Problems stream as `problem` SSE events, are capped at 500 per job and saved to `job_problem` (`GET /api/jobs/{id}/problems`). The same parser rebuilds module progress from `Reactor Build Order`, `Building <name> <version> [n/m]` and the `Reactor Summary` (keys are module names); extension events replace it as soon as they arrive. Compare: `#/compare/{a}/{b}` (History checkboxes, or Compare on the job page for the previous run) shows both runs, total time change, argument differences (`-pl` as added/removed modules), new and gone problems, and per-module times sorted by the biggest change. Fixtures 09 (Surefire failures) and 10 (threaded compile error) are generated from `testdata/sample-maven`.
   - **Done when:** on the phase 0 fixtures the parser finds all compile errors and failed tests; on a live build progress and timings match the `Reactor Summary`.
5. **Package scanner**
   - **Done when:** a large project's tree builds within the target time, rescans use mtime, split packages are highlighted.
6. **"Build changed"**
   - **Done when:** on a branch with edits in two modules exactly those two modules are offered; "since the last successful run" works; a root pom change gives "build everything".
7. **Polish (v2)**
   - Autostart and CLI `buildyard run <task>` (both done early, 2026-11-18), system notifications on completion, log cleanup, opening files in the IDE, Gradle adapter, selective `target/` cleanup for failed modules, rebuilding FAILURE+SKIPPED instead of `-rf` (see SPIKE.md).

## Testing, risks, open questions

The tool's reliability rests on three pure functions: the command builder, the output parser and the file-to-module mapping. They are covered by table tests from day one.

**Tests:**

- Unit (Go table tests): `BuildCommand` (RunSpec → argv), `OutputParser` on fixtures from `testdata/`, file-to-module mapping, discovery output parsing, executable resolution, JDK detection, package scanner on a mini tree.
- Integration (build tag `integration`): a small three-module Maven project in `testdata/sample-maven/` (one module fails to compile when a property is set), with real mvn and extension runs.
- HTTP: `httptest` on routes, SSE with `Last-Event-ID`.
- A manual checklist on a large real project at the end of each phase — its done criterion.

| Risk | Why it hurts | What we do |
| --- | --- | --- |
| mvnd ignores `-Dmaven.ext.class.path` or caches the extension between builds | No events or timings with mvnd | Check once mvnd is installed; fallback log parser; version in the jar name |
| Module discovery is slow | The module tree loads slowly | `help:active-profiles` offline (5.5 s for 952 modules); background task; cache in the DB |
| Output format changes between Maven 3.9 and Maven 4 | The fallback parser breaks | The main channel is extension events; fixtures for both versions |
| Parallel builds of different projects write to a shared local repository | Random failures | `maxConcurrentJobs = 1` by default |
| The daemon runs commands over HTTP | Code run from another web page | 127.0.0.1 only, only saved shell commands; token and Host / Origin checks deliberately removed |
| The daemon as a service does not see `PATH` and `JAVA_HOME` from the shell | mvn / java not found | mvn/mvnd lookup in Homebrew/sdkman dirs; JDK list in settings; `/api/tools` shows what was found |
| The project grows into "another CI" | Nothing gets finished | Phases with criteria; anything outside the requirements table goes to `docs/IDEAS.md` |

Open questions:

- [x] Which Maven and JDK does Dremio use now? Maven 3.9.16 via `./mvnw`, JDK 21.0.8+ (see SPIKE.md). The extension's `--release 11` is fine.
- [x] mvnd or mvn by default? `auto`: the project's `./mvnw` first, then mvnd, then mvn; configurable globally and per project.
- [ ] Which OS does the daemon run on: macOS, Linux or both? Affects autostart and notifications. (The user's machine is macOS arm64.)
- [ ] Is Gradle needed soon, or is the interface enough?
- [ ] Name: check whether "Buildyard" is free if the project goes public.
