# Contributing to HYDRA-UMC-JOB-DISPATCHER 🦾

We welcome contributions to the task allocation engine of the HYDRA-UMC platform.

## Technology Stack
- **Runtime**: Node.js 20+.
- **Database**: Redis (State), SQLite (Persistence).
- **Architecture**: Event-Driven, Microservices.
- **Protocols**: WebSocket, gRPC.

## Guidelines
1. **Event Consistency**: Ensure all mission state transitions are atomic and reflected in the Redis store.
2. **Prioritization Logic**: Any changes to the priority scheduler must be tested against high-throughput scenarios to avoid mission starvation.
3. **Tool Registry**: When adding new URTC tool types, update the dispatcher's routing logic to handle tool-specific job requirements.
4. **Fault Tolerance**: Validate that mission states can be recovered from SQLite after a full service restart.
