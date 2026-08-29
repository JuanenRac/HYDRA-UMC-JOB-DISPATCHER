# HTTP API Reference

Real, plain JSON/HTTP surface (`net/http`, no framework) implemented in
[`src/api/api.go`](../src/api/api.go), over the scheduling logic in
[`src/dispatcher/dispatcher.go`](../src/dispatcher/dispatcher.go). One
`Engine` instance, safe for concurrent use (internal mutex).

No authentication - internal/same-network use.

---

## `GET /jobs`

Lists every job currently known to the engine.

**Response** - `200`, a JSON array of `Job`:

```json
[
  {
    "ID": "weld-42",
    "Priority": 5,
    "RequiredTool": "Laser",
    "DependsOn": ["prep-42"],
    "Status": "blocked",
    "AssignedRobot": ""
  }
]
```

`Status` is one of `"pending"`, `"blocked"`, `"assigned"`, `"done"`, `"failed"`, `"unreachable"` - computed by the engine, never caller-supplied (see `POST /jobs` below). `"unreachable"` means a `dependsOn` entry itself ended `"failed"` (or is itself `"unreachable"`) - unlike `"blocked"`, which just means "still waiting", an `"unreachable"` job can never become eligible on its own; it only resolves back to `"pending"`/`"blocked"` if that dependency is retried (see `POST /jobs/submit`) and eventually succeeds.

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
| 409 | `{"error": "job ID already exists: \"<id>\""}` | A job with that `id` already exists. |
| 409 | `{"error": "dependency job ID does not exist: job \"<id>\" depends on \"<dep>\""}` | A `dependsOn` entry references a job ID that was never submitted - caught at submission time, not left to silently never become eligible. |

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
| 200 | the existing `Job` + `"result": "duplicate"` | A job with this `dedupKey` is already `"pending"`, `"blocked"`, `"assigned"`, or `"done"` - returned **unchanged**. The submitted `id`/`priority`/etc. are ignored; nothing new is scheduled. |
| 200 | the reset `Job` + `"result": "retried"` | A job with this `dedupKey` previously ended `"failed"` - it is reset to `"pending"` under its **original `id`**, refreshed with this submission's `priority`/`requiredTool`/`dependsOn`. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |
| 409 | same error shapes as `POST /jobs` | No matching `dedupKey` on record and the plain insert failed (ID collision or unknown dependency), or a `"retried"` reset was attempted with an unknown `dependsOn` entry. |

```bash
curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2","priority":5,"dedupKey":"req-abc"}'
# -> 201 {"ID":"job-2", "Status":"pending", ..., "result":"created"}

curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2-retry","priority":5,"dedupKey":"req-abc"}'
# -> 200 {"ID":"job-2", "Status":"pending", ..., "result":"duplicate"} - job-2, untouched
```

---

## `POST /jobs/complete`

Marks an assigned job as finished (success or failure).

**Request body**

```json
{"id": "weld-42", "success": true}
```

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 200 | the updated `Job` | `Status` becomes `"done"` (if `success: true`) or `"failed"` (if `success: false`). Either way, every other job's `dependsOn` is re-evaluated: completing to `"done"` may flip a `"blocked"` dependent to `"pending"`; completing to `"failed"` may instead flip one or more downstream dependents (transitively, through a whole multi-step chain) to `"unreachable"` - it can never happen on its own. |
| 400 | `{"error": "job ID does not exist"}` | Unknown `id`. |
| 400 | `{"error": "job is not in the assigned state"}` | The job exists but was never dispatched (still `"pending"`/`"blocked"`) or is already `"done"`/`"failed"`. |

---

## `GET /robots`

Lists every robot in the fleet registry.

**Response** - `200`, a JSON array of `Robot`:

```json
[
  {"ID": "arm-3", "Location": "cell-2", "Tool": "Laser", "Available": true, "Load": 4}
]
```

`Load` is a fairness counter: how many jobs this robot has completed this session (see `POST /dispatch` below for how it's used) - it is never set directly by `POST /robots`, only incremented internally when a job it was assigned reaches `"done"` or `"failed"`.

## `POST /robots`

Registers a new robot, or updates an existing one's `location`/`tool`/`available` fields (its `Load` fairness counter is preserved either way - not resettable via this endpoint).

**Request body**

```json
{"id": "arm-3", "location": "cell-2", "tool": "Laser", "available": true}
```

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 200 | the full updated robot list (same shape as `GET /robots`) | Registered/updated. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |

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

## Errors

Any other path/method returns Go's default `405 Method Not Allowed` (with an `Allow` header) for a known path with the wrong verb, or `404 page not found` (stdlib `net/http` default) for an unknown path.
