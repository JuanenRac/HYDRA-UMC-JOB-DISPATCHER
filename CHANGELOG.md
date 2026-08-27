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
