# HotMouse graph protocol

Sapper's `intent` maps to a HotMouse Step; its `doc` maps to fact detail and
`result.detail/evidence`. HotMouse owns SQLite transactions, claims, provider
recovery and worker result commits. Do not create a `.sapper/` store, run
`sapper setup`, install project hooks or start a tmux commander.

Use the `detail_access` commands/URLs provided in context to read facts, steps,
findings, flags and historical evidence. The service's authenticated CLI may
need the operator's connection configuration; bundled KB commands and files
are available offline and do not require a service connection.

The Dispatcher returns the planning schema in the enclosing prompt. An
executor returns exactly one `accepted/data` JSON envelope, using a concise
`title` and `description`, plus:

```json
{"result":{"type":"progress","detail":"exact observations and artifact paths","evidence":{"request_file":"absolute path","response_file":"absolute path"}}}
```

Use `negative` for confirmed target-side negatives, `blocked` for a target-side
obstacle, `environment_failure` for local tool/provider failures,
`no_information` for no added evidence, and `needs_attention` when the step
cannot continue under the current constraints. Findings describe confirmed
security vulnerabilities or flags, not an environment failure.

For a captured flag, return `finding.kind=flag` and `finding.data.value` with
the exact value and optional `finding.data.challenge_id`. HotMouse commits the
result, its fact and the flag ledger together. Workers must not separately
conclude their Step or directly write the fact/flag tables.

After a confirmed reusable method, an operator or explicitly assigned step can
save an experience through `hotmouse experience create`: title, conditions,
method, failure_conditions, tags and source project/step/fact IDs. Keep new
target facts separate from prior project IDs. The bundled KB is a static
reference; project evidence and learned experience stay in SQLite.
