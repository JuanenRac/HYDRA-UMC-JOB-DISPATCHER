# Security Policy 🔒 (HYDRA-UMC-JOB-DISPATCHER)

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.x.x  | ✅ Yes             |

## Reporting a Vulnerability

**CRITICAL: Do not report safety-critical vulnerabilities through public GitHub issues.**

In a production dispatching system, a security flaw can lead to factory-wide downtime or unauthorized mission injection. If you discover a vulnerability affecting the **Redis state integrity**, **WebSocket mission hijacking**, or **priority escalation**:

1. **Email**: Send a detailed report to `electrohobby3d@gmail.com`.
2. **Impact**: Describe if the bug allows injecting unauthorized jobs, modifying mission parameters in-flight, or crashing the job queue.
3. **Response**: Initial acknowledgment within 48 hours.

We follow a coordinated disclosure policy to ensure hardware safety before public release.
