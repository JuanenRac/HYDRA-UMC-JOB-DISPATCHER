<!-- =============================================================================
HYDRA-UMC-JOB-DISPATCHER - Health service over gRPC
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Health service over gRPC

The dispatcher can serve the `HealthService` of the shared contract (`hydra_common.proto`, package `hydra.common.v1`, kept in HYDRA-UMC-ORCHESTRATOR) so that HYDRA-UMC-NODE-HEALING asks `Check()` and follows `Watch()` instead of polling `GET /health`. It is **off by default**: nothing listens until the configuration file (`-config`) turns it on. It only reads the queue; it assigns, cancels and changes nothing. The HTTP API stays as it is, and `GET /health` keeps answering.

```json
{
  "health_grpc": {
    "enabled": true,
    "listen": "127.0.0.1:50090",
    "token_env": "JOB_DISPATCHER_HEALTH_TOKEN",
    "max_connections": 16,
    "max_watchers": 8,
    "watch_interval_seconds": 5,
    "keepalive_seconds": 30
  }
}
```

Every key but `enabled` is optional and an unknown key stops the start. A change of this section needs a restart (`POST /admin/reload-config` does not move the listener).

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Without `true` the section does nothing, whatever else it says. |
| `listen` | `127.0.0.1:50090` | A host and a port. Loopback unless you say otherwise. |
| `token_env` | none | Environment variable that holds the bearer token. On an address other than loopback the token of the HTTP API (`-token`) is used when this is not named, and without any token the service refuses to start. |
| `max_connections` | 16 | Connections open at once (1 to 1024). One past the limit is closed at once. |
| `max_watchers` | 8 | `Watch()` streams open at once (1 to 1024). One more is answered `RESOURCE_EXHAUSTED`. |
| `watch_interval_seconds` | 5 | A stream repeats the report at least this often when nothing changed (0.1 to 60). It is the keepalive of the stream. |
| `keepalive_seconds` | 30 | Period of the HTTP/2 pings; a client that does not answer within the same time is dropped (1 to 3600). |

## The identity of the server

When the service is started with `-identity-key-file` (the key of HYDRA-UMC-SERVER, the same file that opens the HTTP API to the signed token of the server), the gRPC health service accepts that signed token as a bearer token too, in `authorization: Bearer <token>`, next to the token named by `token_env` (which keeps working). A token of any role is enough: the service only reads. Nothing is open any more: a call with no credential is refused, even on loopback, and an address reachable from the network needs no token of its own. Without the key file nothing changes.

## What it says

`Check()` returns one `HealthReport`; `Watch()` returns the same report as a stream: the current one at once, another whenever the state or the detail changes, and a repeat every `watch_interval_seconds`, until the client cancels or the service stops.

| `HealthState` | Read like `grpc.health.v1` | When |
|---|---|---|
| `HEALTH_STATE_OK` | `SERVING` | The queue answers and, with `-db`, the last write to the store went through. |
| `HEALTH_STATE_DEGRADED` | `SERVING` with a concern | The store reported an error: jobs are still routed, but transitions are being lost (the same condition `GET /health` answers with `503`). |
| `HEALTH_STATE_UNHEALTHY` | `NOT_SERVING` | The queue does not answer within two seconds. |
| `HEALTH_STATE_UNSPECIFIED` | `UNKNOWN` | Never sent by this service. |

The identity is `HYDRA-UMC-JOB-DISPATCHER` with the version of the binary. The metrics carry `jobs_pending`, `jobs_assigned`, `jobs_unknown` and `persistent` (numbers and a flag, never a job, a robot or a token); the detail never carries the text of a store error. The report is built at most once a second however many streams read it, and a queue that stopped answering is probed by one goroutine at a time, so a stuck queue cannot pile them up.

## Pointing the watchdog at it

HYDRA-UMC-NODE-HEALING takes a registry entry with an `address` (gRPC) instead of a `healthUrl`. The `name` has to be exactly the name the service answers with, or the watchdog treats the node as invalid:

```json
{ "name": "HYDRA-UMC-JOB-DISPATCHER", "address": "127.0.0.1:50090", "watch": true }
```

`"watch": true` makes the watchdog open `Watch()`; a service that does not serve it is polled with `Check()` as before.

The watchdog dials without credentials and without TLS today, so it can only reach a service that is on this machine and has no token (the default). A token or a network address works for any other client that sends `authorization: Bearer <token>`, but not yet for the watchdog.

## Generated code and limits

* `src/healthpb` holds the Go bindings of the contract, generated with `protoc`, `protoc-gen-go` and `protoc-gen-go-grpc` from the same `hydra_common.proto` (`--go_opt=paths=source_relative --go-grpc_opt=paths=source_relative`). It is not edited by hand.
* Messages from the client are limited to 1 KiB, and every goroutine of a stream ends when its client goes away or the service stops.
* There is no TLS: the transport is plain HTTP/2, so use a loopback address or a network you trust.
* It has been tested only against a gRPC client of its own in the tests, never against a live HYDRA-UMC-NODE-HEALING, a robot or a cell.
