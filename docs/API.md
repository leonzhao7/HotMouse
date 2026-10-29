# HotMouse Backend API

The backend is implemented in Go with Iris and GORM, using SQLite by default.
It exposes only the following route patterns:

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/project` | List projects |
| `POST` | `/project` | Create a project with its goal and facts |
| `GET` | `/project/{id}/export` | Download the project graph as JSON |
| `GET` | `/project/{id}/goal` | Read the project's immutable goal node |
| `POST` | `/project/{id}/goal` | Complete a project after a step reaches its goal |
| `GET` | `/project/{id}/fact` | List fact nodes |
| `POST` | `/project/{id}/fact` | Append a fact node |
| `GET` | `/project/{id}/step` | List directed steps |
| `POST` | `/project/{id}/step` | Append a directed step |
| `GET` | `/settings` | Read the LLM settings |
| `PUT` | `/settings` | Replace the LLM settings |
| `GET` | `/llm-log` | List the latest persisted Dispatch LLM interactions |

Graph data has no update or delete endpoints. `/settings` is the only mutable
resource. Project details are available from `GET /project`; goal, facts, and
steps are read from their project subresources. Export a complete graph using
`GET /project/{id}/export`.

The API process also runs a background dispatcher. Creating a project or
appending a fact or step queues that project for LLM reasoning. The dispatcher
selects a promising action, runs it through the local worker, summarizes the
worker result into append-only facts and steps, and completes the project when
the resulting graph reaches the goal. Dispatch work is serialized per
project, and graph updates are committed transactionally.

## Create a project

`POST /project` requires a name, one goal, and at least one fact. New projects
start open with no steps. The `POST /project/{id}/goal` operation completes the
project and requires at least one incoming step to the goal.

Names, goal content, and fact content must be non-blank after trimming
whitespace. Description is optional. Creation is transactional: a failed node
insert rolls back the project and all of its initial nodes. The `201` response
contains project metadata, the goal node, the fact nodes with their IDs, and
an initially empty `steps` array.

```json
{
  "name": "Product launch",
  "description": "Optional context",
  "goal": "Launch the product",
  "facts": ["Requirements are approved", "A prototype is available"]
}
```

## Add nodes and steps

Append a fact with `POST /project/{id}/fact`:

```json
{"content": "A new fact"}
```

Append a step with `POST /project/{id}/step`:

```json
{"from_node_id": 2, "to_node_id": 3, "description": "Optional note"}
```

`description` is optional; it is trimmed and stored as an empty string when
omitted or blank. Use node IDs returned by project creation, fact creation, or
the goal endpoint. The origin must be a fact in the same project; the
destination may be a fact or the project's goal. Each unordered pair of
distinct nodes can have only one directed step: after A → B exists, both a
duplicate A → B and a reverse B → A are rejected. Self-steps and cross-project
steps are also rejected. Longer directed cycles between facts are allowed; the
graph is not required to be acyclic.

Complete a project by sending `POST /project/{id}/goal` with an empty body. The
operation returns `409 Conflict` until at least one existing step points to
that project's goal. Once completed, the project is terminal and no additional
facts or steps may be appended. Repeating the completion request is idempotent
and returns the original completion timestamp. Completion changes project
status, not the goal node.

Successful creation operations return `201 Created`. Invalid graph operations
return `400`, unknown project or node IDs return `404`, and duplicate steps
return `409`. Responses use JSON.

`GET /project` returns an array of project metadata (`id`, `name`, optional
`description`, `status`, `created_at`, and optional `completed_at`). The goal
endpoint returns one node. Fact and step endpoints return arrays sorted by ID;
an empty list is `[]`, not `null`. Node objects contain `id`, `project_id`,
`kind`, `content`, and `created_at`. Step objects contain `id`, `project_id`,
`from_node_id`, `to_node_id`, `description` (omitted when empty), and
`created_at`.

`GET /project/{id}/export` returns the same project graph shape as project
creation (`id`, project metadata, `goal`, `facts`, and `steps`) as a formatted
JSON attachment named `project-{id}.json`. Facts and steps are sorted by ID;
an empty list is `[]`. Unknown project IDs return `404` with the standard JSON
error envelope.

## LLM settings

`GET /settings` returns the singleton configuration. Before anything has been
saved it returns empty LLM connection fields, default prompt templates, a
`max_iterations` value of `8`, and no `updated_at`.

`PUT /settings` replaces all fields and returns the stored configuration. The
legacy `llm_url`, `api_key`, `model`, and `max_tokens` fields remain aliases for
the worker profile. Existing clients that send only those fields continue to
work; dispatch then uses the same LLM profile.

```json
{
  "llm_url": "https://api.openai.com/v1",
  "api_key": "secret",
  "model": "gpt-4o-mini",
  "max_tokens": 1024,
  "worker_llm_url": "https://api.openai.com/v1",
  "worker_api_key": "secret",
  "worker_model": "gpt-4o-mini",
  "worker_max_tokens": 1024,
  "worker_command": "my-worker --prompt {{prompt}}",
  "dispatch_llm_url": "https://api.openai.com/v1",
  "dispatch_api_key": "secret",
  "dispatch_model": "gpt-4o-mini",
  "dispatch_max_tokens": 1024,
  "max_iterations": 8,
  "worker_prompt": "Project goal: {{goal}}\\nSelected fact: {{fact}}\\nPlanned action: {{action}}",
  "dispatch_plan_prompt": "Choose an action using this graph: {{graph_json}}",
  "dispatch_summary_prompt": "Summarize {{worker_output}} for graph {{graph_json}}"
}
```

The worker and dispatch LLM profiles each configure URL, API key, model, and
maximum tokens independently. Empty dispatch URL/model/token fields inherit
the worker values; an empty API key means no authentication when a dispatch
profile is provided. If all dispatch profile fields are omitted, the complete
worker profile is reused. Each max-token value must be between `1` and
`10000000`. `max_iterations` must be between `1` and `100`.

`worker_command` optionally starts an external Worker process for the actual
planned action. The command is split into arguments without invoking a shell,
so shell operators are not interpreted. `{{prompt}}` is replaced with the
actual Worker prompt; the same prompt is also written to standard input. An
empty command keeps the built-in OpenAI-compatible Worker. Dispatch planning
and summarization always use the configured Dispatch LLM.

The worker prompt supports `{{goal}}`, `{{fact}}`, `{{fact_id}}`, and
`{{action}}`. The dispatch plan prompt supports `{{graph_json}}`. The dispatch
summary prompt supports `{{graph_json}}`, `{{chosen_action_json}}`, and
`{{worker_output}}`. Empty prompt values restore their built-in defaults.
Settings persist in SQLite and are returned as stored, including API keys and
`updated_at`; keys are not masked. Only `GET` and `PUT` are registered on this
path.

## LLM logs

`GET /llm-log` returns the latest 200 persisted Dispatch LLM interactions. Each
item contains the request and response on one row, including the operation,
URL, model, configured maximum tokens, prompt, response content, HTTP status,
actual prompt/completion/total token counts, and any error. API keys are never
stored in LLM logs. Worker LLM calls and external Worker commands are not
recorded.

## Validation and errors

Send JSON bodies with `Content-Type: application/json`. Unknown fields,
malformed JSON, non-object JSON, and multiple JSON documents are rejected.
Bodies are limited to 1 MiB (`413`); unsupported content types return `415`.
The completion endpoint accepts either no body or an empty JSON object and
never accepts replacement goal content. IDs must be positive integers within
the platform's signed integer range. Unsupported methods return `405`; paths
outside the registered route patterns return `404`.

Errors use this shape:

```json
{
  "error": {
    "code": "goal_unreachable",
    "message": "at least one step must point to the goal before completion"
  }
}
```

Graph validation uses `invalid_graph` (`400`), missing resources use
`not_found` (`404`), duplicate node pairs use `duplicate_step` (`409`), and
attempts to append to a completed project use `project_completed` (`409`).
Settings validation uses `invalid_settings` (`400`). Unexpected storage
failures return a generic `internal_error` (`500`).

## Storage guarantees

GORM initializes the SQLite schema automatically. A partial unique index limits
each project to one goal, and a unique index on normalized node pairs prevents
both duplicate and reverse steps, including concurrent inserts. SQLite checks
and triggers enforce valid node kinds, project-local endpoints, goal direction,
and the completion precondition. Nodes and steps cannot be updated or deleted,
including through replacement/upsert SQL. Completing a project only updates
its status and completion timestamp; it does not modify its graph.

SQLite writes use immediate transactions and a five-second busy timeout. LLM
settings live in a singleton row and are the only mutable data; the graph
tables keep their append-only triggers. Data and constraints survive
application restarts. There are no graph update or delete routes, only the
`/settings` replacement endpoint. The frontend also provides `/logs` for
persisted Dispatch LLM interactions.

## Run

```sh
./start.sh
```

`start.sh` defaults to port `8865`; pass another port as its first argument,
for example `./start.sh 9000`. The script starts the service from `backend`,
so the default database is `backend/hotmouse.db`. When running the Go binary
directly, `HTTP_ADDR` defaults to `:8080` and `DATABASE_DSN` defaults to
`hotmouse.db`. Go 1.23+ and a C compiler with CGO enabled are required.

## Verify

Run from `backend`:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
```

Tests cover required creation fields, atomic rollback, appending facts,
fact-to-fact and fact-to-goal edges, step descriptions, project isolation, both
directions of pair uniqueness, self-step rejection, goal-origin rejection,
completion validation and idempotence, SQL-level immutability, concurrent
database connections, legacy schema migration, persistence across reopen,
settings defaults and replacement, settings validation, route restrictions,
and JSON/error handling.
