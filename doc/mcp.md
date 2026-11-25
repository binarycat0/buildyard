# MCP for agents

The daemon serves an [MCP](https://modelcontextprotocol.io) endpoint (streamable HTTP) at `http://localhost:7777/mcp`, on the UI's port.

## Setup

Claude Code:

```sh
claude mcp add --transport http buildyard http://localhost:7777/mcp
```

opencode (`opencode.json`):

```json
{ "mcp": { "buildyard": { "type": "remote", "url": "http://localhost:7777/mcp" } } }
```

## Tools

| Tool | Does |
| --- | --- |
| `discover` | Inspects a folder: build tool, wrapper, git, executable and JDK it would use. |
| `create_project` | Adds a project and starts module discovery. |
| `create_task` | Saves a task: goals, modules, profiles, skips, `-D`, extra args. |
| `list_projects` | Projects with git branch, worktrees and tasks. |
| `list_modules` | Modules with the ref for `-pl`; `filter`, `refresh`. |
| `run_task` | Runs a task. Per-run overrides: `worktree`, `modules`, `also_make`, `properties`, `skip`, `dont_skip`, `extra_args`. |
| `get_job` | Status, failed modules, problems, log tail; `wait_seconds`, `log_from`. |
| `list_jobs` | Recent jobs of a project. |
| `rerun_job` | Runs a job again; `without_failed`. |
| `cancel_job` | Cancels a job. |
| `get_settings` | Tools, JDKs, defaults, config files; with `project`, what its runs use. |
| `read_config_file` | One config file, passwords masked. |

Agents can add projects and tasks but cannot edit or delete them, or change settings.

Example, one test class: `run_task {project: "app", task: "Quick", modules: [":core"], properties: {test: "FooTest"}, dont_skip: ["tests"]}`, then `get_job {job: 42, wait_seconds: 300}`.
