# Keep the Mac awake

On macOS, Buildyard can stop the Mac from idle-sleeping while a build runs, so long builds are not interrupted.

Turn it on in **Settings → General → Keep the Mac awake while builds run**. It is off by default and saved as `keepAwake = true` in `~/.buildyard/config.toml`. The checkbox only shows on macOS.

## How it works

Right after a build process starts, Buildyard runs

```sh
caffeinate -i -w <build pid>
```

next to it. `-i` holds an idle-sleep assertion; `-w` makes caffeinate exit as soon as the build process exits. The build's command, process group and Stop / Kill are unchanged, and caffeinate ends with the build even if the daemon stops.

- The job log gets a line `[buildyard] keeping the Mac awake: caffeinate -i -w <pid>`, and `daemon.log` logs both process ids.
- A running build kept awake shows ☕ next to its status.
- Check it with `pmset -g assertions | grep caffeinate` while a build runs.

Closing the lid still puts the Mac to sleep; only idle sleep is prevented.
