# HotMouse backend

Local single-process backend for HotMouse. It uses Go, Iris and SQLite.

```bash
cd backend
HOTMOUSE_DB=./data/hotmouse.db go run ./cmd/hotmouse
```

The server listens on `:8080` by default. Set `HOTMOUSE_ADDR` to change it. Local authentication is enabled by default. Get an arithmetic challenge from `GET /api/auth/captcha`, then include its `captcha_id` and answer in `POST /api/auth/register` or `POST /api/auth/login`; the session is returned as an HttpOnly cookie. Set `HOTMOUSE_AUTH_REQUIRED=0` only for an isolated local development session.

Core routes expose the project graph protocol (`/projects`, `/steps`, `/intents`, `/decide`, `/reason`). Product routes include `/api/vulnerabilities`, `/api/templates`, `/api/settings`, `/api/workers`, `/api/audit`, and `/api/notifications`. The local worker executes a configured command through `POST /api/projects/{project_id}/steps/{step_id}/execute`; with an empty command it records a deterministic mock result.

The local worker runs commands in a per-project cwd. `/api/workers/config` accepts `workspace_root`, `completed_action` (`keep` or `remove`), and `context_root`. By default, workspaces live in `workspaces/` beside the SQLite file. HotMouse marks owned directories and removes only matching marked workspaces. When a context root exists, it receives `projects/{project_id}.project-context.yaml`; the default and project YAML are merged into `HOTMOUSE_PROJECT_CONTEXT_YAML` and the worker prompt. This context is guidance, not network egress enforcement.

Set `context_root` or `HOTMOUSE_PROJECT_CONTEXT_ROOT` to an existing context directory. Without an explicit root, HotMouse searches the current directory and its parents for `.hotmouse/context`.

Built-in `codex`, `claude`/`claudecode`, and `pi` drivers use local CLIs. Set `PI_CLI` in the worker environment when Pi is installed outside `PATH`; the worker test and health summary verify CLI availability only, not provider credentials or model API access.

Reports, report templates/attachments, Docker/container deployment, and multi-worker scheduling are intentionally outside this migration.

Set `HOTMOUSE_AUTORUN=1` to start the serialized local worker loop. Without it, use the explicit `/api/projects/{project_id}/steps/{step_id}/execute` endpoint while developing or integrating another local trigger.
