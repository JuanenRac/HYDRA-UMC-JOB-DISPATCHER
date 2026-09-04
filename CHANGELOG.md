# Changelog

All notable work on **HYDRA-UMC-JOB-DISPATCHER** is summarized here, newest first. Full
session-by-session detail (including dates) lives in a private,
unpublished internal log - this file is public, so it intentionally
omits calendar dates.

## Versioning scheme

`version.go`'s `Version` constant is bumped automatically by
`bump_version.py`, run from `build.sh`/`build.bat` before every real
release build (`go build`).

It follows the ecosystem-wide base-10 "odometer" rule rather than
semantic-versioning judgment calls:

- `PATCH` +1 on every build
- when `PATCH` would exceed 9, it resets to 0 and `MINOR` +1 instead (e.g. `0.0.9` -> `0.1.0`, never `0.0.10`)
- the same carry cascades into `MAJOR` if `MINOR` would exceed 9

---

## [0.1.0] - Fixed: a retried dependency never un-stuck its Unreachable dependents

- **`dispatcher.go`** - found in a live ecosystem bug audit: `SubmitJob()`'s
  retry path (a previously `Failed` job resubmitted with the same
  `DedupKey`) only ever recomputed the retried job's own `Status` - it
  never called `refreshBlocked()`, the only mechanism that re-evaluates
  *other* jobs' eligibility. A dependent already marked `Unreachable`
  because this job had failed stayed `Unreachable` forever after the
  retry, even though the retried job went straight back to `Pending` -
  directly contradicting `Job.Status`'s own documented contract for
  `StatusUnreachable` ("Resolves back to Pending/Blocked if that
  dependency is later retried"). Reproduced with a real repro (`pick`
  fails, `place` becomes `Unreachable`; `pick` is retried via
  `SubmitJob` - `place` stayed `Unreachable` instead of returning to
  `Blocked`) and confirmed fixed: the retry path now calls
  `refreshBlocked()`, the same fixed-point re-evaluation `CompleteJob()`
  already used for the Done/Failed transitions.
- New test (`TestSubmitJob_RetryUnsticksDependentFromUnreachable`) - full
  suite passing.

## Documentation - Real HTTP API reference

- **`docs/API.md`** (new) - every real endpoint (`GET`/`POST /jobs`,
  `POST /jobs/complete`, `GET`/`POST /robots`, `POST /dispatch`)
  documented from the actual handler code in `api.go` and the scheduling
  semantics in `dispatcher.go`: request/response bodies, every status
  code, and precisely how `Status`/`Load`/dependency-unblocking behave.
  Cross-checked against `api_test.go`'s real status-code assertions.
  Documentation-only - no code changed, no version bump.

## Unreleased - validated job identity and dependency graph

- **`dispatcher.go` / `api.go`** - direct engine callers and both submit
  routes now reject blank job IDs, self-dependencies and repeated dependency
  IDs. Invalid request topology is returned as HTTP 400 rather than being
  stored as work that can never become eligible.
- Added engine and HTTP regression coverage for the rejected inputs.

---

## [0.1.0]

- Build version synchronized with `hydra-umc.project.json` and the repository-native version source.

## [0.0.9] - Real CM5 deployment

- **`systemd/hydra-umc-job-dispatcher.service`** (new) - loopback-only
  unit for `HYDRA-UMC-OS/provisioning/install_job_dispatcher.sh` (new,
  that repo), which builds this pure-Go binary on-device (no cgo
  dependency, so no C toolchain beyond `golang-go` itself is needed).
  Real gap found auditing the ecosystem against actual CM5 hardware: the
  priority mission queue and its real HTTP API (`src/api`,
  `src/dispatcher`) had never been built or installed anywhere.

## [0.0.8] - Real ecosystem live-status opt-in

- **`hydra-umc.project.json`** declares its real `service.port` (8090)
  and `health_path` (`/jobs`) - HYDRA-UMC-SERVER's ecosystem status
  endpoint now does a real HTTP GET against it (expecting 2xx) instead
  of only reporting static manifest metadata.

## [0.0.7] - Fixed: a job stuck `Blocked` forever behind a permanently failed dependency

- **`dispatcher.go`** - found in a live ecosystem bug audit: `computeStatus()` only ever distinguished "dependency still unfinished" from "dependency `Done`", so a dependent job whose dependency ended `Failed` stayed `Blocked` forever - no state transition, no error, nothing visible via the API to tell an operator the job was permanently stuck rather than legitimately waiting. A multi-step mission (e.g. a `place` job depending on a `pick` job) whose first step failed left every later step wedged, since `DispatchOnce()` only ever considers `Pending` jobs and `refreshBlocked()` could only promote a `Blocked` job once *every* dependency reached `Done`, never `Failed`.
- New `StatusUnreachable` status: `computeStatus()` now returns it as soon as any `DependsOn` entry is itself `Failed` or `Unreachable` - a real, queryable-via-the-API distinction from `Blocked` (which still means "will resolve on its own, given time") and from `Failed` (which means "this job itself ran and failed", not "one of its dependencies did"). `refreshBlocked()` now loops to a fixed point instead of a single pass, so an `Unreachable` verdict propagates through an entire multi-step dependency chain in one `CompleteJob()` call rather than only the immediate next stage. It also re-evaluates `Unreachable` jobs, not only `Pending`/`Blocked` ones, so a dependent un-sticks on its own if the failed dependency is later retried (`SubmitJob`, already supported) and succeeds.
- **`docs/API.md`** - documented `"unreachable"` alongside the existing `Status` values, and updated the `POST /jobs`/`POST /jobs/complete` sections to describe when a job starts or transitions into it.
- 2 new tests (`TestRefreshBlocked_DependencyFailureMakesDependentUnreachable`, `TestRefreshBlocked_UnreachablePropagatesThroughMultiStepChain`) - full suite (23 tests across both packages) passing.

## [0.0.6] - Idempotent job submission: real deduplication and safe retry

- **`dispatcher.go`** - `Job` gains an opt-in `DedupKey` field and `Engine` gains `SubmitJob()`, a new idempotent entry point alongside the existing `AddJob()` (unchanged: still always inserts, still errors on an ID collision). `SubmitJob()` treats a repeated `DedupKey` as the same logical unit of work: a job that's Pending, Blocked, Assigned, or already Done is returned **unchanged** on a repeat submission (`SubmitDuplicate`) - the real guarantee this closes is that a client retrying a request it's unsure was received (a timed-out HTTP call, for example) can never cause the same job to be scheduled or run twice. A job that previously ended `Failed` is instead reset to `Pending` under its **original job ID** (`SubmitRetried`), refreshed with the retry's own `Priority`/`RequiredTool`/`DependsOn` - a genuine retry-after-failure keeps its whole history under one ID instead of minting a new one.
- **`api.go`** - new `POST /jobs/submit` route wraps `SubmitJob()` (`201` on a real creation, `200` on a duplicate or a retry, with a `result` field in the response body); the existing `POST /jobs` route is untouched, still backed by `AddJob()`, same response shape as before for every current caller.
- Deterministic priority ordering (already true in practice - `DispatchOnce()` sorts by `Priority` descending, then submission order ascending as a stable tie-break) is now covered by an explicit regression test that rebuilds the same job set from scratch 20 times and asserts the dispatch order never varies - guards against a future accidental dependency on Go's randomized map iteration order.
- **`build.sh`** - fixed a version double-bump: the manifest-sync step ran `bump_manifest_version.py` without `--sync` *before* the native `bump_version.py` step, so `version.go` advanced twice per build while the manifest advanced once. Reordered to bump native first, then `--sync` after (matching `build.bat`'s already-correct order). Also added a `go test ./...` step to both `build.sh` and `build.bat` - previously neither actually ran the test suite as part of a real build, despite advertising "verification" in their own banner text.
- 10 new tests (`TestSubmitJob_*`, `TestDispatchOnce_PriorityOrderIsDeterministicAcrossRepeatedRuns`, `TestHandleSubmitJob_*`) - 22 total, all passing. Verified live against the real running HTTP server: a duplicate submission returned the original job untouched, a retry-after-failure reused the same job ID and reset it to `Pending`, and `GET /jobs` showed exactly one job throughout.

## [0.0.4] - Source layout: `src/` instead of `internal/`, unused folders removed

- Moved `internal/dispatcher` and `internal/api` (added in 0.0.2) to
  `src/dispatcher` and `src/api` - this repo's real source now lives
  where the README always said it would (`src/`), rather than
  introducing a second, competing location. `main.go`/`version.go` stay
  at the repo root as the entry point.
- Removed the empty, unused `docs/`, `images/` and `scripts/` folders while
  there was nothing real to put in them; real content can recreate them later.
- `run.sh`/`run.bat` now forward CLI arguments (`"$@"`/`%*`) to the
  compiled binary - a gap from 0.0.2 (the README already showed
  `run.bat -addr ...`, but the script silently dropped the flag).
- `build.sh`/`build.bat` and `run.sh`/`run.bat` no longer close their
  window immediately on exit: `build`/`run.bat` now `pause` (including
  on a failed build), and `build`/`run.sh` set an `EXIT` trap that
  prompts before closing - but only when stdin is actually a terminal
  (`[ -t 0 ]`), so CI/piped/non-interactive runs are unaffected.
- Verified for real: `go build ./...`, `go vet ./...` and `go test ./...`
  all clean after the move (import paths updated accordingly); `build.sh`
  run for real end-to-end (version bump + compile) after the trap change,
  confirmed it does not hang non-interactively.

## [0.0.2] - Real scheduler: priority queue, tool-aware routing, multi-stage dependencies

- **`src/dispatcher`** - the real engine: a global job queue where
  `DispatchOnce()` matches the highest-priority eligible job (ties broken
  FIFO) to the best available robot with a matching `Tool` and the
  lowest `Load` (spreads work across the fleet instead of piling onto
  one robot). A job with `DependsOn` stays `blocked` until every
  dependency reaches `done` - multi-stage missions ("Pick" before
  "Place") are enforced, not just documented. `CompleteJob` frees the
  robot and re-evaluates every blocked job in case this completion just
  unblocked the next stage.
- **`src/api`** - plain JSON/HTTP surface (stdlib `net/http`, no
  framework) over the engine: `POST /robots`, `POST /jobs`,
  `POST /dispatch`, `POST /jobs/complete`, `GET /jobs`, `GET /robots`.
- **`main.go`** - now wires the engine to the API and listens on
  `-addr` (default `:8090`), instead of only printing identity and
  exiting.
- Verified for real: `go vet ./...` clean; 12 `go test ./...` cases
  (scheduling algorithm unit tests in `src/dispatcher` + real HTTP
  round-trips via `httptest` in `src/api`) covering tool-aware
  routing, priority bypass, multi-stage dependency blocking/unblocking,
  and fleet load-balancing. Additionally smoke-tested the compiled
  binary end-to-end with real `curl` requests against a real listening
  port: register a robot, submit a job, dispatch it, complete it, and
  confirm the robot's `Load` incremented and it became available again.
- Corrected a stale "Tech: Node.js / Redis" badge (all 7 README
  languages) - this project has always actually been Go, per its own
  `go.mod`; the badge now reads "Go / net/http".
- What's still not real: persistence (Redis/DB, per the README) - see
  `src/dispatcher`'s own doc comment for why the scheduling
  algorithm needed to be proven correct first; and the tool-availability
  check is registration-based, not yet confirmed against a real URTC
  over CAN.

## [0.0.1] - Initial scaffolding

- **`main.go`** - minimal real entry point (prints identity/version/role, exits 0). No dispatch logic yet - job queueing/scheduling across the cell's available robots lands in a later pass.
- **`version.go`** - version identity (`Version` constant).
- **`build.sh` / `build.bat`**, **`run.sh` / `run.bat`** - `go build` and run the resulting binary.
