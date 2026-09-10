# Contributing to HYDRA-UMC-JOB-DISPATCHER 🦾

We welcome contributions to the task allocation engine of the HYDRA-UMC platform.

## Technology Stack
- **Language**: Go (see `go.mod` for the toolchain version).
- **HTTP**: the standard library `net/http` - no web framework.
- **Persistence**: opt-in, pure-Go SQLite via `modernc.org/sqlite` (no CGO), in `src/sqlitestore`. Unset (`-db` not passed), the queue is purely in-memory.
- **Layout**: `src/dispatcher` (the scheduling engine), `src/api` (JSON/HTTP handlers), `src/sqlitestore` (persistence); `main.go`/`version.go` at the repo root wire them together.
- **API style**: plain JSON over HTTP. gRPC (`hydra.common.v1`) is reserved for node-to-node traffic and is not used here.

## Guidelines
1. **Prioritization logic**: any change to the priority scheduler must keep ordering deterministic across repeated runs (there is an explicit regression test for this) and avoid mission starvation.
2. **Tool registry**: when adding new URTC tool types, update the dispatcher's routing logic to handle tool-specific job requirements.
3. **Deduplication**: `AddJob()`/`POST /jobs` stays an insert-or-error primitive; layer new idempotency behaviour on `SubmitJob()`/`POST /jobs/submit` via `Job.DedupKey`.
4. **Fault tolerance**: validate that mission state can be recovered from the SQLite store after a full service restart (`GET /health` must report a degraded store via `LastPersistError()` rather than failing silently).
5. **Testing**: run `go vet ./...`, `go test ./...` and `go build ./...`, plus `python tools/ci_validate.py`, before opening a pull request.
