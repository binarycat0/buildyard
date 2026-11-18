# Phase 0 spike: building Dremio with Maven

Date: 2026-11-17. Subject: the Dremio monorepo (`dremio-global/dremio`), HEAD `2b42a56` (before `git pull`: `f139f13`).
Goal: learn how to build Dremio without tests, native libraries and UI, how to get the module list, and how to read Maven output. A fully green build was not achieved on this machine; that does not block phase 1. Dremio is only the nearest large example: Buildyard stays generic for any Maven project.

Log fixtures: `testdata/maven-logs/` (see section 8). The full logs stayed outside the repository, in `~/buildyard-spike/`.

## 1. Environment

| | |
|---|---|
| OS | macOS 27.0.1, arm64, 16 cores, 48 GB |
| JDK | OpenJDK 21.0.8 arm64 (Homebrew, via jenv) |
| `./mvnw` | Maven **3.9.16** (wrapper 3.3.4) — use this |
| `mvn` on PATH | Maven 3.9.11 — do not use |
| `mvnd` | not installed |
| `.mvn/maven.config` | `-Drevision=28.0.0-SNAPSHOT` (CI-friendly version, changes on `git pull`) |
| `.mvn/jvm.config` | `-Xms1024m -Xss2048k -XX:+UseParallelGC` + `--add-exports/--add-opens jdk.compiler/...` |
| `.mvn/extensions.xml` | `maven-profiler` (enabled with `-Dprofile`), `maven-notifier`, `opentelemetry-maven-extension` (disabled) |

**JDK.** One JDK drives the build; there is no split of modules between 17 and 21. The enforcer requires JDK `[21.0.8,22)` and Maven `[3.9.7,4)`. Before commit `3f5d0f7` (2026-10-22) the build used `maven.compiler.release=17`; now it is 21. Toolchains (`~/.m2/toolchains.xml`, JDK 11/17/21) are only needed for tests (`dremio.test-jdk.version`). Temurin 21.0.7 as the main JDK would fail the enforcer.

## 2. Build command

Sources: `dremio-global/AGENTS.md`, `dremio/dremio2-shortcuts.sh` (the `drci` function) and `README.md`.

```bash
cd dremio-global/dremio
ulimit -n $(sysctl -n kern.maxfilesperproc)
./mvnw -B clean install -DskipTests -Dlicense.skip -Dmaven.javadoc.skip=true \
  -Ddremio.no-native=true -Ddremio.no-ui -T 1.5C \
  -Dmaven.repo.local=/Users/<user>/dremio-global/.m2repo
```

Notes:
- **`.m2repo`**: the workspace's local repository, not `~/.m2`. `~/.m2` holds stale SNAPSHOTs.
- `ulimit -n` raised to the maximum, plus `MAVEN_OPTS=-XX:-MaxFDLimit` (`~/.mavenrc`).
- `-T 1.5C` — same as CI (Jenkinsfile: `install -T 1.5C -DskipTests`).
- `clean` is required: without it, stale `target/` directories cause false failures (section 6).
- In shell scripts pass the command as an array: in zsh a `$CMD` string is not word-split.

## 3. Skip flags

| What | Flag | How it works |
|---|---|---|
| Tests | `-DskipTests` | tests are compiled but not run (including Go and pytest in daas) |
| Native libraries | `-Ddremio.no-native=true` | the `=true` value is required: the `no-native` profile in `joust-cpp`/`tracker-cpp` activates on `!= true` and enables cmake |
| UI | `-Ddremio.no-ui` | sets `skip.uiclean/uibundle/installnodepnpm/pnpm`; 19 UI modules stay in the reactor, but `frontend-maven-plugin` logs "Skipping execution" |
| Lint | `-Ddremio.no-lint` | disables enforcer, spotless, checkstyle, license, forbiddenapis, prettier, errorprone. Lint is **on** in `drci` |
| Licenses | `-Dlicense.skip` | |
| Javadoc | `-Dmaven.javadoc.skip=true` | |
| No Cloud | `-Ddremio.no-daas-build` or `'-P!daas-build'` | removes `daas/` |
| OSS only | `-Ddremio.oss-only=true` | removes extras/community/enterprise/daas |
| Go SDK | `-Dmvn.golang.get.skip=true` | what CI does |
| Distribution/Docker/RPM | off by default | profiles `rpm`, `docker`, `daas-docker-*` only with an explicit `-P` |

## 4. Module discovery

| Method | Time | Modules |
|---|---|---|
| **`./mvnw -B -o help:active-profiles` + flags** | **5.5 s** | 952 + active profiles of each |
| `./mvnw -B -o help:all-profiles` | 5.5 s | all declared profiles |
| `./mvnw -B validate` + flags | 442 s | 952 (runs validate-phase plugins) |
| `exec:exec -Dexec.executable=echo` | fails | resolves dependencies and fails offline; the short `exec:` prefix also does not resolve offline |

Recommendation: `help:active-profiles`. It needs `maven-help-plugin` in the local repository, otherwise network access.
Default modules: OSS + Community + Enterprise + DaaS. Root pom → `oss`; `oss/pom.xml`: profile `with-extras-module` (`dremio.oss-only != true`) → `extras` (→ `community`, `enterprise`, `openapi`); `extras/pom.xml`: profile `daas-build` → `daas`.

## 5. Maven output with `-T`

- Lines from different modules are interleaved.
- `-Dorg.slf4j.simpleLogger.showThreadName=true` → each module line gets a `[mvn-builder-<artifactId>]` prefix; `[main]` is the reactor level. That is enough to attribute output to modules.
- All `-Dorg.slf4j.simpleLogger.*` options work (Maven 3.9.16, `maven-slf4j-provider`): `showDateTime`, `dateTimeFormat=HH:mm:ss.SSS`, `log.<logger>=<level>`. `...log.org.apache.maven.cli.event.ExecutionEventLogger=warn` removes the banners.
- Reactor Summary: long module names have no dot padding (`<name> FAILURE [ 18.775 s]`); times come as `NN.NNN s` and `MM:SS min`; `SKIPPED` lines have no time.
- With `-T`, a module's time includes waiting for CPU (module times sum to 36077 s against 1736 s wall clock). Finding slow modules needs line timestamps or `-Dprofile`.
- `mvnd -B` output format not checked: mvnd is not installed.

## 6. Problems found (failure classes for Buildyard)

1. **x86 grpc plugin in Quarkus modules.** Quarkus 3.27.x (BOM) pins `io.grpc:protoc-gen-grpc-java` 1.69.1; for `osx-aarch_64` that version is an x86_64 binary (universal binaries start at 1.77). x86 code does not run on this machine now, while on 2026-09-22 (before macOS 27.0.1 was installed on 2026-10-26) the same binary worked. 4 modules fail: `Dremio Catalog Optimization - Sharable Test Code`, `Polaris Server - Sharable Test Code`, `Polaris Server - Common gRPC`, `DaaS Comics Saga - Deployment`. Symptom: `program not found or is not executable` in `quarkus-maven-plugin:generate-code`. Quarkus properties can only override `protoc` (`quarkus.grpc.protoc-path`); there is no property for the grpc plugin.
2. **Stale `target/` without `clean`.**
   - forbiddenapis fails in `Common - Core` on `UrlUtils.class`, whose source no longer exists.
   - Compile error in `ee-sabot-kernel`: the `PrivilegeService.getOwnerIds` signature does not match.
   - Buildyard should be able to clean `target/` selectively — only for failed modules, without touching a built distribution.
3. **The `-rf` trap with `-T`.** SKIPPED modules can come earlier in reactor order than the first failed module. `-rf :<failed>` does not rebuild them and picks up old jars from the local repository → false compile errors. The right approach is to rebuild FAILURE+SKIPPED from the previous run (`-pl ...`).
4. **Excluding a module with `-pl '!…'`** breaks its dependents if the repository has no artifact of the current version (after `git pull` the version went from 27 to 28).
5. **False `[ERROR]`.** `gosdk-wrapper` logs `[ERROR] Can't load GoSDK ... 403`, then downloads successfully from `go.dev`. Detect failures from the Reactor Summary and `Failed to execute goal`, not from `[ERROR]`.

## 7. Measurements

| Run | HEAD | Conditions | Time | Result |
|---|---|---|---|---|
| 1 | f139f13 | `~/.m2`, `-T 1C`, no-lint | 6:11 | 569 OK / 2 FAIL / 381 SKIP (of 952) |
| v2 | 2b42a56 | `.m2repo`, `-T 1.5C`, no clean, lint on | 6:15 | failed on forbiddenapis (stale `target/`) |
| v3 | 2b42a56 | same + no-lint | 28:56 | 506 OK / 2 FAIL / 451 SKIP |
| v4 | 2b42a56 | same + `-fae -pl '!<2 grpc modules>'` | 25:27 | 825 OK / 4 FAIL / 128 SKIP (of 957) |
| drci (clean) | — | | — | not run: `mvn clean` was not permitted in that session |

## 8. Fixtures (`testdata/maven-logs/`)

Email and home path are replaced (`user@example.com`, `/Users/dev`); the middle of each log is trimmed and marked `# ... trimmed ...`. Each file starts with `# buildyard fixture:` lines describing it.

| File | Contents |
|---|---|
| `01-success-module-threaded.log` | reactor start + all lines of one successful module (`dremio-common-core`) with `[mvn-builder-…]` |
| `02-compile-error.log` | lines of `dremio-ee-sabot-kernel` + the final compile `[ERROR]`s (javac, `required/found/reason`) |
| `03-plugin-failure-quarkus-generate-code.log` | `quarkus-maven-plugin:generate-code` failure (x86 grpc plugin) |
| `04-reactor-summary-failure-skipped.log` | Reactor Summary for 957 modules with SUCCESS/FAILURE/SKIPPED, `BUILD FAILURE`, all errors (`-fae`) |
| `05-reactor-summary-plain-no-threadnames.log` | same without thread prefixes (plain `-T 1C` output) |
| `06-false-error-gosdk-fallback.log` | interleaved `-T` output with a false GoSDK `[ERROR]` and a successful fallback |

## 9. Open questions

1. How are the 4 Quarkus modules with the x86 grpc plugin built today? Should Buildyard mark them as "needs x86" and exclude them together with their dependents?
2. Run `drci` (`clean install`, lint on) and confirm the stale-`target/` errors go away.
3. `-T 1C` vs `-T 1.5C` on 16 cores; a repeat run with a warm `.m2repo`.
4. mvnd: install it for comparison?
5. `-Dmaven.test.skip=true` (no test compilation) vs test-jar dependencies — not checked.
