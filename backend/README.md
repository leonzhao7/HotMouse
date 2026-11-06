# HotMouse backend

Local backend for HotMouse. It uses Go, Iris and SQLite. Each active project has one Dispatcher that owns project planning and commits agent results to the fact graph. A configurable pool of Agent Workers runs external agents to execute concrete steps; workers persist their results for the project Dispatcher to commit.

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

Each active project has one Dispatcher that owns planning, recovery summaries, and committing worker results. Agent Workers execute existing steps only. Each worker identity has a separate cwd under `workspaces/{project_id}/workers/`; the project root retains shared project files and ownership markers. Project `max_workers` defaults to 1 and can be set from 1 to 32 when creating a project or at `/projects/{project_id}/worker-limit`; the API enforces this cap while claiming steps. `/api/workers/config` controls the global pool size, `workspace_root`, `completed_action` (`keep` or `remove`), and `context_root`. When a context root exists, it receives `projects/{project_id}.project-context.yaml`; the default and project YAML are merged into `HOTMOUSE_PROJECT_CONTEXT_YAML` and the worker prompt. This context is guidance, not network egress enforcement.

Set `context_root` or `HOTMOUSE_PROJECT_CONTEXT_ROOT` to an existing context directory. Without an explicit root, HotMouse searches the current directory and its parents for `.hotmouse/context`.

Worker artifact sharing uses a directory and prompt convention. Each worker keeps all new task data, scripts, evidence and outputs in its own project subdirectory, browses the project for reusable peer artifacts, and leaves peers' files unchanged. Commands run with normal host filesystem access; HotMouse adds no mount namespace, OS write restriction, or `bubblewrap` dependency. Workers receive `HOTMOUSE_PROJECT_DIR`, `HOTMOUSE_WORKSPACE_DIR`, peer workspace locations and a bounded artifact listing; `TMPDIR`, `TMP` and `TEMP` point into the worker's own directory. Record absolute evidence paths so other workers can reuse them.

Planning workspace context supplies `step_output_directory: ${HOTMOUSE_WORKSPACE_DIR}` instead of the Dispatcher's writable directory. Planned step descriptions use this literal placeholder for new outputs, for example `${HOTMOUSE_WORKSPACE_DIR}/zip_list.txt`. When a worker executes the step, HotMouse replaces the placeholder in `HOTMOUSE_STEP_DESCRIPTION` and the execution prompt with that worker's directory. Graph records and existing input/evidence paths retain their original locations. Execution guidance also requires relocating new outputs from older plans that name a different worker's directory; recovery summaries preserve recorded paths and perform no new actions.

The Dispatcher commits saved successful results first, then processes summaries that have not failed yet. Failed summaries have a persistent per-step retry deadline and yield to independent executable or working steps and planning from newly committed results. Retry attempts rotate instead of one interrupted step occupying the queue. An interrupted session without a committed result does not trigger an extra Decide while its summary is deferred; the existing event and unchanged-graph checks still govern planning.

Built-in `codex`, `claude`/`claudecode`, and `pi` drivers use local CLIs. Set `PI_CLI` in the worker environment when Pi is installed outside `PATH`; the worker test and health summary verify CLI availability only, not provider credentials or model API access.

Reports, report templates/attachments and Docker/container deployment are outside this migration.

Set `HOTMOUSE_AUTORUN=1` to start project Dispatchers and the Agent Worker pool. Set `worker_count` through `/api/workers/config` to control the global number of concurrent external agents; restart the service to apply a changed pool size. Each project's `max_workers` caps its own share of that pool and applies immediately. Without autorun, use the explicit `/api/projects/{project_id}/steps/{step_id}/execute` endpoint while developing or integrating another local trigger. Each external process start is logged to backend stdout with its project, step, worker identity and argv.

Planning includes every historical action, regardless of status, and atomically rejects duplicate step descriptions after normalizing whitespace and case. Before planning or execution, the Dispatcher asks the LLM whether the project is a remote CTF requiring target monitoring. Custom command adapters must support `HOTMOUSE_TASK_TYPE=target_probe` and return `required`, `reason`, and `targets` (host/port pairs). HotMouse probes those TCP endpoints in a separate goroutine using 20-second windows; three consecutive failures for any required endpoint force the project to fail, even while work is in flight. Successful connections reset that endpoint's counter. Startup analysis and counters survive service restarts; resuming a project starts a fresh analysis.

Compact model contexts, target probing contracts, structured execution outcomes, event-triggered Decide, route stop-loss controls, history/experience recall and flag records are described in [strategy.md](../docs/strategy.md). Existing databases migrate on startup; complete YAML exports remain available.
