# buildyard

A personal build manager for Maven projects: a local daemon with a web UI.

Documentation: https://tools.binaryc.at/buildyard

## Quick start

macOS or Linux, from a release (no Go needed):

```sh
curl -fsSL https://raw.githubusercontent.com/binarycat0/buildyard/main/install.sh | sh
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
