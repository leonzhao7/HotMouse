# Route scope

The CLI commands and their route mappings are described in [cli.md](cli.md). Run the HTTP service with `hotmouse serve`; business commands connect through the API.

Core routes:

- `/projects` create/list/read/delete/title/status, complete/reopen, YAML export
- project facts through project detail and `/projects/{id}/facts/{fact}`
- `/projects/{id}/goals`, Goal update/delete/complete
- `/projects/{id}/steps` and `/intents` create/update/heartbeat/release/conclude
- `/projects/{id}/decide` and `/reason` claim/heartbeat/release
- `/projects/{id}/findings`, hints, timeline and campaign synthesis
- `/api/auth`, `/api/settings`, `/api/workers`, `/api/vulnerabilities`
- `/api/templates`, `/api/audit`, `/api/notifications`
- compact model context, history recall, route budgets and stop-loss records, Decide events, shared experiences and flag ledger: see [strategy.md](strategy.md)

`GET /projects/{id}/export` serves the project graph YAML and timeline formats. `/api/templates` includes built-in templates and user-scoped custom templates using `tmpl_<uuid>` IDs. Vulnerability screenshot/evidence uploads, per-vulnerability reports, CSV/Markdown export and export-record routes, plus `/api/report-templates`, are outside the current scope. Vulnerability review and summaries are supported.

Each active project has one Dispatcher for planning, recovery summaries, and committing Agent Worker results. Agent Workers execute steps only. Each worker identity receives a separate cwd under its project's marked workspace and gets `HOTMOUSE_PROJECT_ID`, `HOTMOUSE_STEP_ID`, `HOTMOUSE_STEP_DESCRIPTION`, `HOTMOUSE_WORKSPACE_DIR`, and (when configured) project context variables. `max_workers` on a project defaults to 1, is configurable from 1 to 32 via project creation or `PUT /projects/{id}/worker-limit`, and is enforced transactionally during step claims. Global pool size remains configured through `/api/workers/config`. Empty command uses the mock driver. `completed_action=remove` deletes only workspaces carrying the matching `.hotmouse-project` marker.

Shelving any route immediately fails the whole project and does not request a new decision. Budget/no-information limits and needs_attention use this same rule, even if independent work remains. Route management is available while failed so an operator can repair the cause before explicitly resuming.

After a successful decision, an active project with remaining goals is marked `failed` when the current graph has no executable steps, pending decisions, or in-flight recovery work. The failure reason is included in project details. `project resume` clears the failure and queues a fresh decision.

Planning also stops after three consecutive provider failures, timeouts, rejections, or invalid decisions for the same graph and event batch when no executable or recovering work remains. Counts survive restarts; new input or a successful decision resets the allowance. Pending events from failed planning do not keep the project active indefinitely.

The worker connection test and health summary currently verify local command or CLI availability (`--version` for built-in providers). Provider credentials, model access and network reachability are not validated here. For Pi, `PI_CLI` can name the executable used by provider execution and local CLI health checks.

`GET/PUT /api/workers/config` also supports a separate `dispatcher` provider (`driver`, `command`, `model`, `environment`, and write-only `environment_remove`). Omission preserves its configuration; `null` selects the shared Worker provider. Secrets are masked unless `reveal=1`; revealed responses are not cached. `POST /api/workers/config/test?role=dispatcher` checks that provider locally. Pending steps remain claimable during planning, and plan updates skip steps already claimed or concluded at commit.

Before the first plan or automated step claim, Web startup preprocessing fetches HTTP(S) challenge entry URLs selected by target analysis. Project detail includes `target_probe.preprocessing` with status, fact ID, response paths, byte counts, and request errors. Full headers, redirect responses, and bodies are persisted in the preprocessing fact and Dispatcher workspace. Service restart reuses these results; project resume fetches again.

Lease expiry is observable protocol behavior. Project list/detail, Campaign, and project graph YAML/timeline exports reclaim expired Step and Decide leases. The product timeline, project title/delete, Step patch, and native Goal completion do not reclaim leases. Status changes, legacy project completion, and reopen reclaim only Decide leases; stopping/completing a project also clears open Step workers.

Auth responses use `user_<uuid>` string IDs. Existing integer HotMouse user databases are migrated on startup, including sessions, audit/notification rows, and template ownership. Login sets `session_token` and the historical `hotmouse_session` alias for local clients. Session cookies are HttpOnly and SameSite=Strict; Secure is omitted only for HTTP localhost/loopback requests.

Explicitly excluded from this migration: upstream test cases and test reports, report agent/composer/template export and evidence package generation, Docker/container/egress deployment, and worker pool selection.
