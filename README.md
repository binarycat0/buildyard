# buildyard

A personal build manager for Maven projects: a local daemon with a web UI.

Documentation: https://tools.binaryc.at/buildyard

## Install

macOS or Linux, from a release (no Go needed):

```sh
curl -fsSL https://raw.githubusercontent.com/binarycat0/buildyard/main/install.sh | sh
```

Quick start

```sh
buildyard service install   # launchd agent (macOS) or systemd --user unit (Linux)
buildyard service start     # then open http://127.0.0.1:7777/
```

The script checks the release's SHA-256, installs the binary to `~/.buildyard/bin` and links it from `~/.local/bin` (no sudo). Run it again to upgrade; a running service is restarted. `BUILDYARD_VERSION=v0.1.0` pins a release, `BUILDYARD_BIN_DIR` changes where the link goes. From a checkout, `make install` does the same with a local build.

The service runs `buildyard serve --service` with the `PATH`, `JAVA_HOME` and `BUILDYARD_HOME` of the shell that ran `service install`, so it finds mvn, mvnd and java the way that shell does; run `service install` again after changing them. Its output goes to `~/.buildyard/daemon.log`. `buildyard service stop|restart|status|uninstall` manage it; stopping it interrupts running builds.

To uninstall: `make uninstall`, or `buildyard service uninstall` and then delete `~/.local/bin/buildyard` and `~/.buildyard`.

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


### Build and run

Requires Go 1.26+.

```sh
make build              # bin/buildyard (no cgo needed)
make test               # go vet, go test -race
./bin/buildyard serve   # listens on 127.0.0.1:7777, open http://127.0.0.1:7777/
./bin/buildyard open    # open the UI in the browser
```

A second instance for development: `BUILDYARD_HOME=/tmp/by-test ./bin/buildyard serve --port 7790`.

Releases: push a `v*` tag, or run the **Release** workflow on `main` with a version such as `v0.1.0`. It builds darwin and linux binaries (amd64, arm64) with checksums. CI runs gofmt, vet and race tests on every pull request.

Data lives in `~/.buildyard` (`BUILDYARD_HOME` overrides it): `config.toml`, `buildyard.db`, `daemon.log`. Settings → General edits `maxConcurrentJobs` and `retention.jobsPerProject` in `config.toml` and applies them at once (saving drops comments in the file); after editing the file by hand, `kill -HUP <pid>` reloads it. The port needs a restart.

## License

MIT, see [LICENSE](LICENSE).
