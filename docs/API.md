# HTTP API Reference

Real, plain JSON/HTTP surface (`net/http`, no framework) implemented in
[`src/api/api.go`](../src/api/api.go), over the scheduling logic in
[`src/dispatcher/dispatcher.go`](../src/dispatcher/dispatcher.go). One
`Engine` instance, safe for concurrent use (internal mutex).

## Access

The process listens on `127.0.0.1:8090` unless `-addr` says otherwise. An address that is
reachable from the network (`:8090`, `0.0.0.0:8090`, a LAN address) **needs a bearer token**
(`-token`, or the `JOB_DISPATCHER_TOKEN` environment variable) or an explicit
`-allow-anonymous` (an open lab dispatcher), and the process refuses to start without one of
them. With a token every route except `GET /health` answers `401` unless the request carries
`Authorization: Bearer <token>`. The HTTP server has read/write/idle timeouts, and a request
body over 64 KB is refused with `413` (a malformed body is `400`).

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8090` | Where the API listens. |
| `-token` | `$JOB_DISPATCHER_TOKEN` | Bearer token every route but `GET /health` requires. |
| `-allow-anonymous` | off | Allows an address other than loopback without a token. |
| `-robot-timeout` | `2m` | A robot silent for longer is not offered new work (`0` disables the check). |
| `-db` | unset | SQLite file for persistence (see below). |

## Choosing the robot

`POST /jobs` and `POST /jobs/submit` accept an optional `location`. A job goes to the available
robot with the required tool and the lowest load, **preferring** a robot whose `location` is
the job's (the preference never blocks the job: when no robot there is available, another one
takes it). A robot that has not checked in (`POST /robots`) for longer than `-robot-timeout`
is not eligible: it kept `Available=true` in the registry after it stopped answering and went
on receiving jobs. The last heartbeat of each robot is persisted with it (`GET /robots` shows
`LastHeartbeat`), so a restart does not forget when a robot was last heard; a database written
by the previous version is migrated in place, and a robot with no recorded heartbeat waits for
its first check-in before it is offered work.

## Persistence

Purely in-memory by default - the mission queue and fleet registry are
lost on a restart, which is fine for local development/testing. Pass a
real file to `main.go`'s own `-db PATH` flag (e.g.
`-db /opt/hydra-umc/job-dispatcher/data/job-dispatcher.sqlite3`, the real
path [`systemd/hydra-umc-job-dispatcher.service`](../systemd/hydra-umc-job-dispatcher.service)
already uses) to make it durable: every job/robot state change writes
through to a real SQLite database (`src/sqlitestore`, pure Go - no CGO),
and the queue is reloaded, in its original order, the next time this
process starts. See [`GET /health`](#get-health) below for how a
degraded store surfaces.

A completion, a cancellation, a requeue and the stale-heartbeat sweep change several records together (the job, its robot and the jobs that depended on it); they are written to SQLite in **one transaction**, so a crash in the middle leaves either all of it or none. The database runs in WAL mode (`synchronous=NORMAL`), which avoids an extra `fsync` per write on an SD card or eMMC. A heartbeat recorded more than one minute **ahead** of the dispatcher's clock (the clock stepped back since, for example a board without a battery clock that booted with a wrong date) is not trusted: that robot is not offered work until it checks in again, and an assignment whose robot has only such a heartbeat is treated as stale.

---

## `GET /health`

A fast liveness/durability probe - answers "is this process up, and is
its own persistence (if configured) currently working".

**Response** - always `200`:

```json
{"status": "ok", "persistenceHealthy": true, "lastPersistError": null}
```

- `status` - `"ok"`, or `"degraded"` (with HTTP `503`) while the store is
  failing, so a probe that only looks at the status code notices a
  dispatcher that is losing transitions.
- `persistenceHealthy` - `false` once a real Store write has failed and
  hasn't yet been followed by a successful one (see
  `dispatcher.Engine.LastPersistError()`'s own doc comment for exactly
  which failures this covers - a `-db`-less deployment is always `true`,
  since there is nothing to fail).
- `lastPersistError` - a generic message when `persistenceHealthy` is
  `false`, otherwise `null`. The underlying error text stays in the
  process log: this route needs no token and the text can hold paths.

---

## `GET /jobs`

Lists the jobs known to the engine, oldest submission first. Without parameters it returns all of them.

| Parameter | Meaning |
|---|---|
| `status` | Only the jobs in that status (`pending`, `blocked`, `assigned`, `done`, `failed`, `unreachable`, `unknown`, `cancelled`); another value is a `400`. |
| `limit` | At most this many jobs (a positive whole number). |
| `offset` | Skip this many jobs first. |

The `X-Total-Count` header says how many jobs matched the `status` filter, whatever `limit` and `offset` cut, so a client can read a long queue in pieces. A machine-readable description of every route is in [`openapi.yaml`](openapi.yaml).

**Response** - `200`, a JSON array of `Job`:

```json
[
  {
    "ID": "weld-42",
    "Priority": 5,
    "RequiredTool": "Laser",
    "DependsOn": ["prep-42"],
    "Status": "blocked",
    "AssignedRobot": "",
    "SubmittedAtMs": 1735689600000,
    "AssignedAtMs": 0,
    "CompletedAtMs": 0,
    "Attempt": 0
  }
]
```

`SubmittedAtMs`, `AssignedAtMs` and `CompletedAtMs` are unix milliseconds of the dispatcher's clock (`0`: it has not happened yet): when the job was submitted (or retried), when it was last given to a robot (cleared by a requeue) and when it ended (done, failed or cancelled). Queue time and cycle time are the differences. `Attempt` counts how many times the job has been given to a robot; see `POST /jobs/complete`.

`Status` is one of `"pending"`, `"blocked"`, `"assigned"`, `"done"`, `"failed"`, `"cancelled"`, `"unreachable"`, `"unknown"` - computed by the engine, never caller-supplied (see `POST /jobs` below). `"unreachable"` means a `dependsOn` entry itself ended `"failed"` (or is itself `"unreachable"`) - unlike `"blocked"`, which just means "still waiting", an `"unreachable"` job can never become eligible on its own; it only resolves back to `"pending"`/`"blocked"` if that dependency is retried (see `POST /jobs/submit`) and eventually succeeds. `"unknown"` means the job was `"assigned"` to a robot whose heartbeat then went stale with no completion ever reported (see `POST /jobs/detect-stale` below) - the dispatcher genuinely cannot tell whether it succeeded, is still running, or was lost with the robot; it only ever resolves via a real, later `POST /jobs/complete` for that same job.

## `GET /jobs/{id}`

One job, in the shape `GET /jobs` lists. `404` for an unknown id. Use it instead of downloading the whole list to learn the state of one job.

## `POST /jobs`

Submits a new job to the queue.

**Request body**

```json
{
  "id": "weld-42",
  "priority": 5,
  "requiredTool": "Laser",
  "dependsOn": ["prep-42"]
}
```

- `id` (string, required) - must be unique.
- `priority` (int) - higher runs first.
- `requiredTool` (string, optional) - must exactly match a robot's `Tool`; empty/omitted means any available robot qualifies.
- `dependsOn` (array of strings, optional) - job IDs that must already exist and must reach `"done"` before this job becomes eligible. A job with unfinished dependencies starts as `"blocked"`, not `"pending"` - or, if a dependency already ended `"failed"` (or is itself `"unreachable"`), it starts `"unreachable"` instead.

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 201 | the created `Job` (see `GET /jobs`'s shape) | Job accepted; `Status` is `"pending"` or `"blocked"` depending on `dependsOn`. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |
| 400 | `{"error": "invalid job: job ID must not be empty"}` | `id` is whitespace-only (e.g. `" "`) - rejected at the engine level even though it passed the HTTP layer's plain `req.ID == ""` check. No `id` is echoed back here - there's nothing meaningful to quote. |
| 400 | `{"error": "invalid job: job \"<id>\" has an empty dependency"}` | A `dependsOn` entry is `""` or whitespace-only. |
| 400 | `{"error": "invalid job: job \"<id>\" cannot depend on itself"}` | A `dependsOn` entry equals the job's own `id`. |
| 400 | `{"error": "invalid job: job \"<id>\" repeats dependency \"<dep>\""}` | The same `dependsOn` entry appears more than once. |
| 409 | `{"error": "job ID already exists: \"<id>\""}` | A job with that `id` already exists. |
| 400 | `{"error": "dependency job ID does not exist: job \"<id>\" depends on \"<dep>\""}` | A `dependsOn` entry references a job ID that was never submitted - caught at submission time, not left to silently never become eligible. |

---

## `POST /jobs/submit`

The idempotent counterpart to `POST /jobs`: submits work identified by an
opt-in `dedupKey` instead of relying solely on the caller picking a unique
`id`. Built for a client that may retry a submission (a timed-out HTTP
response, a process restart mid-request) and needs a guarantee that the
same logical job is never scheduled or run twice.

**Request body**

```json
{
  "id": "weld-42",
  "priority": 5,
  "requiredTool": "Laser",
  "dependsOn": ["prep-42"],
  "dedupKey": "req-9f2a"
}
```

Same fields as `POST /jobs`, plus:

- `dedupKey` (string, optional) - identifies the logical unit of work behind this submission, independent of `id`. Omitted or empty means no deduplication: behaves exactly like `POST /jobs`.

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 201 | the created `Job` + `"result": "created"` | No job with this `dedupKey` existed yet - a real new job was inserted, same as `POST /jobs`. |
| 200 | the existing `Job` + `"result": "duplicate"` | A job with this `dedupKey` is already `"pending"`, `"blocked"`, `"assigned"`, `"unknown"`, `"unreachable"` or `"done"` - returned **unchanged**. The submitted `id`/`priority`/etc. are ignored; nothing new is scheduled. |
| 200 | the reset `Job` + `"result": "retried"` | A job with this `dedupKey` previously ended `"failed"` or `"cancelled"` - it is reset to `"pending"` under its **original `id`**, refreshed with this submission's `priority`/`requiredTool`/`location`/`dependsOn`. Dependents that were `"unreachable"` because of it go back to `"blocked"`. |
| 400 | `"invalid job: ... (dependency cycle)"` | The retry's `dependsOn` would make the job depend on itself through other jobs; the job is left untouched. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |
| 400 | same `"invalid job"` shapes as `POST /jobs` (whitespace-only `id`, empty/self/repeated `dependsOn` entry) | Same job-shape validation as `POST /jobs`, applied before dedup lookup. |
| 409 | same error shapes as `POST /jobs` | No matching `dedupKey` on record and the plain insert failed (ID collision or unknown dependency), or a `"retried"` reset was attempted with an unknown `dependsOn` entry. |

```bash
curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2","priority":5,"dedupKey":"req-abc"}'
# -> 201 {"ID":"job-2", "Status":"pending", ..., "result":"created"}

curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2-retry","priority":5,"dedupKey":"req-abc"}'
# -> 200 {"ID":"job-2", "Status":"pending", ..., "result":"duplicate"} - job-2, untouched
```

---

## `POST /jobs/complete`

Marks an assigned (or `"unknown"` - see `POST /jobs/detect-stale` below) job as finished (success or failure).

**Request body**

```json
{"id": "weld-42", "success": true, "robotId": "arm-3", "attempt": 1}
```

`attempt` is optional: the `Attempt` the job had when the robot received it. A report for an older assignment than the current one (the job was requeued and given again) is a `409` and changes nothing, so the late report of an abandoned assignment cannot close the new one. Omitted or `0`, it is not checked.

`robotId` is required and must match the job's own real `AssignedRobot` - without it, any caller naming just a job ID and a success flag could report completion for a job assigned to a DIFFERENT robot, silently corrupting that other robot's own `Load`/`Available` bookkeeping.

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 200 | the updated `Job` | `Status` becomes `"done"` (if `success: true`) or `"failed"` (if `success: false`). Either way, every other job's `dependsOn` is re-evaluated: completing to `"done"` may flip a `"blocked"` dependent to `"pending"`; completing to `"failed"` may instead flip one or more downstream dependents (transitively, through a whole multi-step chain) to `"unreachable"` - it can never happen on its own. The robot becomes `Available` again only if it isn't already busy with a different job it picked up while this one sat `"unknown"`. |
| 400 | `{"error": "\"robotId\" is required"}` | `robotId` was missing or empty. |
| 404 | `{"error": "..."}` | Unknown job `id` (or the robot it is assigned to no longer exists). |
| 409 | `{"error": "..."}` | The job exists but is not `"assigned"` (never dispatched, or already finished) - `"unknown"` is accepted, everything else is not. |
| 409 | `{"error": "..."}` | `robotId` does not match the job's own real `AssignedRobot`, or `attempt` is not the job's current one - the job/robot state is left completely untouched, not partially applied. |

Clients written before `robotId` became required (it answers `400` without it) must send it: the robot a job was given by `POST /dispatch` is in the `AssignedRobot` field of `GET /jobs/{id}`.

---

## `POST /jobs/cancel`

Withdraws a job nobody has been given.

```json
{"id": "weld-42"}
```

| Status | Meaning |
|---|---|
| 200 | The updated `Job`. `"pending"`, `"blocked"` and `"unreachable"` become `"cancelled"` (terminal, never dispatched, also not after a restart); cancelling a `"cancelled"` job again is a no-op. Dependents become `"unreachable"`. |
| 400 | `"id"` missing. |
| 404 | Unknown job. |
| 409 | The job is `"assigned"` or `"unknown"` (it may be running on a robot; only that robot's own report ends it, or `POST /jobs/requeue`), or it already finished. |

## `POST /jobs/requeue`

The operator's way out of a job whose robot will never report (a destroyed or replaced robot): an `"assigned"` or `"unknown"` job goes back to `"pending"` with no robot, ready for the next `POST /dispatch`, and the robot it was given to is held unavailable until its own next heartbeat says `available: true`, so the same dead robot is not handed the job again.

```json
{"id": "weld-42", "robotId": "arm-3", "reason": "node declared down"}
```

`robotId` is optional; when present it must be the robot the job is assigned to.

| Status | Meaning |
|---|---|
| 200 | The updated `Job` (also when it was already `"pending"`, `"blocked"` or `"unreachable"`). |
| 400 | `"id"` missing. |
| 404 | Unknown job. |
| 409 | The job already finished (`"done"`, `"failed"`, `"cancelled"`), or `robotId` is not the robot the job is assigned to. |

---

## `GET /robots`

Lists every robot in the fleet registry.

**Response** - `200`, a JSON array of `Robot`:

```json
[
  {"ID": "arm-3", "Location": "cell-2", "Tool": "Laser", "Available": true, "Load": 4}
]
```

`Load` is a fairness counter: how many jobs this robot has finished this session, failed ones included, so a robot that fails everything it is given does not stay the least-loaded one and keep attracting the queue (see `POST /dispatch` below for how it's used) - it is never set directly by `POST /robots`, only incremented internally when a job it was assigned reaches `"done"` or `"failed"`.

## `POST /robots`

Registers a new robot, or updates an existing one's `location`/`tool`/`available` fields (its `Load` fairness counter is preserved either way - not resettable via this endpoint). It is also the heartbeat: a field left out keeps the value the registry already holds, so a bare `{"id": "arm-3"}` heartbeat does not wipe the tool, location or availability.

**Request body**

```json
{"id": "arm-3", "location": "cell-2", "tool": "Laser", "available": true}
```

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 200 | the full updated robot list (same shape as `GET /robots`) | Registered/updated. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |

## `DELETE /robots/{id}`

Forgets a robot (decommissioned, or registered by mistake). Answers `200` with the registry without it, `404` for an unknown robot and `409` while a job is still `assigned` or `unknown` on it (the outcome of that job would have nobody to be reported by; end it with `POST /jobs/complete` or `POST /jobs/requeue` first). A robot that is still alive registers itself again with its next heartbeat. Requires the token when one is set, like every route except `GET /health`.

---

## `POST /dispatch`

Runs one scheduling pass: every eligible (`"pending"`) job, highest `Priority` first (oldest submission breaks ties), is matched to the best available robot - matching `Tool` (or any robot if `RequiredTool` is empty) with the lowest `Load`, so work spreads across the fleet rather than piling onto one robot. A job with no matching robot right now stays `"pending"` and is reconsidered on the next `/dispatch` call.

No request body.

**Response** - `200`, a JSON array of `Assignment` (empty array `[]` if nothing was assigned this pass):

```json
[{"JobID": "weld-42", "RobotID": "arm-3"}]
```

Each assigned job's `Status` becomes `"assigned"` and `AssignedRobot` is set; the matched robot's `Available` becomes `false` until `POST /jobs/complete` (or a `POST /robots` update) changes it.

---

## `POST /jobs/detect-stale`

Marks every `"assigned"` job whose robot has not sent a real heartbeat (a `POST /robots` call) within `timeoutSeconds` as `"unknown"` - see `Status`'s own description above. A caller may invoke this with its own timeout.

The last heartbeat of every robot is persisted, so a robot that went silent before a restart can still be flagged. A robot with no recorded heartbeat at all is never flagged stale on its own account.

The process also runs this check by itself, every half of `-robot-timeout`, when that flag is not `0`; calling this route is only needed for a different timeout.

Marking a job `"unknown"` does not touch its robot's `Available` field: a robot whose only job just became `"unknown"` is immediately eligible for new work on its own next heartbeat reporting `available: true`, rather than being stranded over one unresolved outcome. If that stale job's own late, genuine report later arrives via `POST /jobs/complete`, it resolves normally - without disturbing whatever newer job the robot has since picked up.

**Request body**

```json
{"timeoutSeconds": 300}
```

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 200 | `{"unknownJobIds": ["weld-42"]}` | The IDs of every job just marked `"unknown"` this pass (`[]` if none). |
| 400 | `{"error": "\"timeoutSeconds\" must be a positive number"}` | Missing, zero, negative, or malformed `timeoutSeconds`. |

---

## Errors

Any other path/method returns Go's default `405 Method Not Allowed` (with an `Allow` header) for a known path with the wrong verb, or `404 page not found` (stdlib `net/http` default) for an unknown path.
