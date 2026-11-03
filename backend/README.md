# HotMouse backend

Local backend for HotMouse. It uses Go, Iris and SQLite. Each active project has one Dispatcher for facts and planning; a configurable pool of Agent Workers executes steps against external agents.

The React workbench is in [frontend](../frontend/README.md). Start this backend, then run `npm ci` and `npm run dev` in `frontend/` and open `http://127.0.0.1:5173`. The frontend proxies both core and product API routes and supports browser registration/login.

The command-line interface and examples are documented in [docs/cli.md](../docs/cli.md). The binary runs a service with `serve`; all business commands use its HTTP API. Running `hotmouse` without a subcommand prints help.

```bash
cd backend
go build -o hotmouse ./cmd/hotmouse
./hotmouse serve --db ./data/hotmouse.db --autorun
```

The server listens on `127.0.0.1:8080` by default. Set `HOTMOUSE_ADDR` or `serve --addr` to change it. Local authentication is enabled by default. `./hotmouse auth register --username operator` prompts for an arithmetic challenge answer and a hidden password, then saves the HttpOnly session cookie in the private client credentials store. Use `auth login` for an existing user. Set `HOTMOUSE_AUTH_REQUIRED=0` or `serve --auth disabled` for an isolated local development session.

In another terminal:

```bash
./hotmouse auth register --username operator
./hotmouse project create --title demo --origin 'local demo target' --goal 'verify and record findings' --use
./hotmouse project campaign --watch
./hotmouse worker status --output json
```

Client configuration uses `os.UserConfigDir()/hotmouse/config.yaml`. `--config`, `--profile`, and `--server` select a connection; credentials and default projects are isolated by service and Profile. JSON/YAML results go to stdout, errors and progress go to stderr. `--select project.id` extracts scalar results for scripts. Deletion and cleanup require `--yes` outside a terminal. `completion bash|zsh|fish|powershell` generates a shell completion script.

The server holds a per-database file lock and cancels HTTP requests, project Dispatchers and Agent Workers before closing on SIGINT/SIGTERM. The SQLite driver requires CGO and a C compiler for a working server build.

Core routes expose the project graph protocol (`/projects`, `/steps`, `/intents`, `/decide`, `/reason`). Product routes include `/api/vulnerabilities`, `/api/templates`, `/api/settings`, `/api/workers`, `/api/audit`, and `/api/notifications`. The local worker executes a configured command through `POST /api/projects/{project_id}/steps/{step_id}/execute`; with an empty command it records a deterministic mock result.

The local worker runs commands in a per-project cwd. `/api/workers/config` accepts `workspace_root`, `completed_action` (`keep` or `remove`), and `context_root`. By default, workspaces live in `workspaces/` beside the SQLite file. HotMouse marks owned directories and removes only matching marked workspaces. When a context root exists, it receives `projects/{project_id}.project-context.yaml`; the default and project YAML are merged into `HOTMOUSE_PROJECT_CONTEXT_YAML` and the worker prompt. This context is guidance, not network egress enforcement.

Set `context_root` or `HOTMOUSE_PROJECT_CONTEXT_ROOT` to an existing context directory. Without an explicit root, HotMouse searches the current directory and its parents for `.hotmouse/context`.

Built-in `codex`, `claude`/`claudecode`, and `pi` drivers use local CLIs. Set `PI_CLI` in the worker environment when Pi is installed outside `PATH`; the worker test and health summary verify CLI availability only, not provider credentials or model API access.

Reports, report templates/attachments and Docker/container deployment are outside this migration.

Set `HOTMOUSE_AUTORUN=1` to start project Dispatchers and the Agent Worker pool. Set `worker_count` through `/api/workers/config` to control the number of concurrent external agents. Without autorun, use the explicit `/api/projects/{project_id}/steps/{step_id}/execute` endpoint while developing or integrating another local trigger. Each external process start is logged to backend stdout with its project, step, worker identity and argv.

Compact model contexts, structured execution outcomes, event-triggered Decide, route stop-loss controls, history/experience recall and flag records are described in [strategy.md](../docs/strategy.md). Existing databases migrate on startup; complete YAML exports remain available.
