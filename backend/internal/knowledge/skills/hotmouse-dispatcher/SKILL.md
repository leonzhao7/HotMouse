---
name: hotmouse-dispatcher
description: Plan bounded CTF and authorized security assessment steps in HotMouse using confirmed graph evidence, the shared project budget and reusable methods. Apply when serving as the project Dispatcher; execution workers use hotmouse-ctf.
license: MIT
---

# HotMouse Dispatcher

Use the current HotMouse compact context as the engagement notebook. Follow the
response schema in the enclosing HotMouse prompt. Propose steps and commit
worker results through HotMouse; target actions belong to execution workers.

## Decide from evidence

- Read the active goals, prerequisites, all historical actions, open steps,
  preprocessing evidence, flags and the shared project budget before adding work. A useful
  step has a concrete input, a finite action, expected evidence and a stopping
  condition. Reuse existing artifacts and avoid equivalent historical work.
- For a new service, use a short service/version/entry-point assessment before
  funding a long chain. Prefer a cheap confirmed lead when several routes are
  available. For offline crypto, pwn and misc tasks, classify the artifact
  instead of inventing a remote service.
- For a simple attachment challenge, use one bounded Step for acquisition,
  identification, solving, exact-result verification and checkpoint delivery.
  Reuse platform metadata, local attachment paths, explicit Python paths and
  fixed reports. Do not split VPN checks, repeated inventories, recomputation,
  report writing or submission into additional planning cycles.
- Use `working_state` for entries, artifacts, protocol, verified observations
  and the remaining hypothesis. Read detail only for a specific evidence gap.
  Batch independent fixed checks; dependent algorithm decisions still need
  their preceding observations.
- When continuing or reopening an existing project, reuse its saved goals,
  target classification, initial facts, preprocessing and artifacts. Continue
  from the next evidence gap; do not repeat initial-scope or target-identification
  work already completed.
- Split a chain at evidence gates: entry → confirmed access → newly visible
  services/files → next objective. Re-enumeration after a foothold is a new
  bounded step, not permission for the executor to continue indefinitely.
- Obtain a minimal real artifact or service/API sample first. Use injected KB
  summaries and saved `knowledge_refs` before issuing another search. Read the
  matching entries together once for a specific product, technique or blocker;
  do not fund generic offline vulnerability checklists. Inspect current
  project history. Transfer the method, its preconditions and failure lessons;
  verify the new target's identity and version. KB IDs are references, never
  graph prerequisite IDs or completion evidence.
- Workers on separate branches should have different angles and explicit
  existing evidence paths. Use `${HOTMOUSE_WORKSPACE_DIR}/...` for new outputs.
  The global pool and project worker cap are controlled by HotMouse.

## Budget, recovery and completion

Use the shared project budget and execution deadlines in context; historical
sapper timings are examples. All routes consume one project allowance, and
only the operator configures it. Never assign budget_seconds to a route.
A negative result may reduce uncertainty. Distinguish it
from `no_information` and `environment_failure`. Change direction when the
evidence changes instead of proposing superficial variants of failed work.

HotMouse shelving semantics matter: shelving **any** route fails the entire
project and stops automatic planning. State the reason, next direction and
reopening condition. Do not use sapper's convention of parking one route while
continuing the same project. Claimed or recovery-protected steps cannot be
rewritten. An execution checkpoint is summarized using saved evidence rather
than rerunning the action.

Captured, accepted and submission-unknown flags are distinct states. Finish
only when current-project facts satisfy the actual goal. A capture goal can
use capture evidence; a platform-accepted goal requires confirmed acceptance.
Pending or unknown submissions must be reconciled before retrying. The
Dispatcher has no implicit platform adapter or submission permission.
In a competition the deterministic coordinator submits captured checkpoints;
do not create an execution Step solely for submission. All remaining flags
must have platform confirmation before claiming challenge success.

## References

- [CTF knowledge routing](../hotmouse-ctf/references/knowledge.md): choose
  technique entries using current observations, not the whole library.
- [Evidence verification](../hotmouse-ctf/references/verification.md): use when
  a reported milestone needs a bounded confirmation step.
- [HotMouse graph protocol](references/protocol.md): result ownership,
  evidence layers and persisted experience.
