# MCP for agents

The running daemon serves an [MCP](https://modelcontextprotocol.io) endpoint at `http://localhost:7777/mcp` (the UI's port; change it if `config.toml` sets another). Coding agents use it to run saved tasks and read the results, without knowing how the project is built. There is nothing to install or start.

## Tools

| Tool | What it does |
| --- | --- |
| `list_projects` | Projects with their directory, git branch, worktrees and saved tasks (command and last job). |
| `run_task` | Queues a saved task. Optional `worktree`, `modules` (`-pl`), `also_make` (`-am`), `properties` (`-D`) and `extra_args` narrow the run, e.g. one test class with `properties: {"test": "FooTest"}`. |
| `get_job` | Status, exit code, failed modules, problems found in the log and the end of the log. `wait_seconds` waits for the job to finish; `log_from` reads around a problem. |
| `cancel_job` | Cancels a queued or running job. |
| `get_settings` | Read-only: build tools, JDKs, global defaults and the tool config files; with `project`, the executable, JDK, env and resources its runs use. |
| `read_config_file` | Read-only: one of those config files (e.g. `maven.settings`), with passwords masked. |

Agents cannot create projects or tasks, or change any settings.

## Setup

Claude Code:

```sh
claude mcp add --transport http buildyard http://localhost:7777/mcp
```

opencode, in `opencode.json`:

```json
{
  "mcp": {
    "buildyard": { "type": "remote", "url": "http://localhost:7777/mcp", "enabled": true }
  }
}
```

Other agents that support remote (streamable HTTP) MCP servers take the same URL.
