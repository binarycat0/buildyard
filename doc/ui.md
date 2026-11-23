# Web UI

The daemon serves the UI at <http://127.0.0.1:7777/> (`buildyard open`).

- **Projects**: the home page lists projects with their last run. **Add project** takes the path to a project root; Buildyard asks Maven for its modules and profiles.
- **Project**: the build tool, JDK and running jobs at the top, then the task list. A task is a saved build: modules, skips, profiles, arguments and resources. **Run** starts it; the ⋯ menu duplicates, deletes or copies its command. The gear opens the project settings (name, JDK, environment, build tool).
- **History**: past runs of the project. Tick two runs and **Compare** them module by module.
- **Job**: the live log, the module tree with progress and timings, problems found in the output, and Stop / Kill / Rerun.
- **Activity**: background work such as module discovery and JDK scans.
- **Settings**: General (builds at once, runs kept, [keep the Mac awake](keep-awake.md)), JDKs, editors for the Maven, mvnd, Gradle and Bazel config files, and [Observability](observability.md).
