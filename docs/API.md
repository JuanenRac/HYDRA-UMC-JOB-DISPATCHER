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

`Status` is one of `"pending"`, `"blocked"`, `"assigned"`, `"done"`, `"failed"` - computed by the engine, never caller-supplied (see `POST /jobs` below).

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
- `dependsOn` (array of strings, optional) - job IDs that must already exist and must reach `"done"` before this job becomes eligible. A job with unfinished dependencies starts as `"blocked"`, not `"pending"`.

**Responses**

| Status | Body | Meaning |
|---|---|---|
| 201 | the created `Job` (see `GET /jobs`'s shape) | Job accepted; `Status` is `"pending"` or `"blocked"` depending on `dependsOn`. |
| 400 | `{"error": "\"id\" is required"}` or a JSON decode error | Missing `id`, or malformed JSON body. |
| 409 | `{"error": "job ID already exists: \"<id>\""}` | A job with that `id` already exists. |
| 409 | `{"error": "dependency job ID does not exist: job \"<id>\" depends on \"<dep>\""}` | A `dependsOn` entry references a job ID that was never submitted - caught at submission time, not left to silently never become eligible. |

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
| 200 | the updated `Job` | `Status` becomes `"done"` (if `success: true`) or `"failed"` (if `success: false`). Completing a job to `"done"` also re-evaluates every other job's `dependsOn` - a job that was `"blocked"` on it may flip to `"pending"`. |
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
