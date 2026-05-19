# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| Latest release | Yes |
| Older releases | No |

We recommend always running the latest release.

## Reporting a vulnerability

If you discover a security vulnerability, please report it responsibly. **Do not open a public issue.**

Email **security@byggflow.com** with:

1. A description of the vulnerability.
2. Steps to reproduce.
3. The potential impact.
4. Any suggested fix (optional).

We will acknowledge your report within 48 hours and aim to provide a fix within 7 days for critical issues.

## Security design

sandboxd is designed to run untrusted code. See the [Security section](README.md#security) in the README for details on:

- Container hardening (Docker + runc)
- gVisor isolation (Docker + gVisor)
- microVM isolation (Firecracker)
- Network isolation
- Access control and multi-tenant identity scoping
- End-to-end encryption
- Network middleware (egress rule injection)
- Agent ↔ daemon authentication

### Agent ↔ daemon authentication

Two complementary defenses protect the agent's listen socket from being used by a hostile party (sandbox process, host-resident worm, or unrelated tenant on the same network):

**Transport-layer peer auth.** Before any application-layer auth runs, the agent's listener rejects connections from illegitimate sources:

- On Firecracker (vsock), only `VMADDR_CID_HOST` (CID 2) is accepted. An in-sandbox process that dials `vsock:VMADDR_CID_LOCAL:9111` is rejected at accept time — no auth.token attempt is even possible.
- On Docker (TCP), loopback addresses (`127.0.0.1`, `::1`) are rejected. The daemon connects via the bridge gateway; any in-container process dialing localhost is non-legitimate.

This means even if a sandbox process learns the long-lived auth token through any vector, it cannot reach the agent to use it.

**Single-use bootstrap nonce.** The long-lived auth token never appears in any guest-readable surface (kernel cmdline, container env, `/proc/cmdline`, `/proc/PID/environ`). Instead:

1. The daemon generates the long-lived token and a single-use bootstrap nonce per sandbox.
2. Only the nonce is delivered to the guest at boot — via kernel cmdline on Firecracker, env var on Docker.
3. The agent unsets `SANDBOX_AUTH_BOOTSTRAP` on startup so child processes spawned via `process.exec` never observe it.
4. On the first daemon connection, the daemon presents the nonce via `auth.bootstrap` and supplies the long-lived token in the same call. The agent verifies the nonce in constant time and stores the token in process memory only.
5. The nonce is consumed (success or failure). All subsequent connections must authenticate with `auth.token` against the in-memory token.

A sandbox process that reads the nonce from `/proc/cmdline` can attempt to use it once — but the daemon has already won the race, the nonce is consumed, and the transport-layer peer check would reject the connection anyway. The long-lived token never appears on disk or in environment, so the cross-leak surface is reduced to daemon process memory (which requires host-equivalent compromise).

### Network middleware threat model

Egress rules installed via `sbx.network.intercept` are stored on the daemon. The agent's in-sandbox HTTP proxy forwards each request to the daemon via `OpNetEgress`; the daemon applies the rule (inject header, deny, allow) and dials the upstream itself.

Properties:

- Credentials injected via `inject.setHeaders` never enter the sandbox memory. The upstream TLS handshake is performed by the daemon, not the sandbox.
- A sandbox cannot observe what was injected by reading its own outbound traffic: the daemon adds headers after the request leaves the sandbox process.
- A sandbox cannot bypass the proxy for plain HTTP because `HTTP_PROXY` is set at container startup and the sandbox runs unprivileged (cannot rewrite `/etc/hosts` or break out of namespacing to dial directly).
- HTTPS is intercepted via a per-sandbox CA: the daemon generates an ECDSA P-256 CA and pushes the certificate to the agent over the authenticated agent connection during the readiness check (single-shot RPC, identical on Docker and Firecracker). The CA never appears in container env vars, kernel boot args, or daemon metadata — only in the agent's `/tmp/sandbox-ca.crt` after install. On `CONNECT`, the agent fetches a short-lived leaf cert for the SNI host (24-hour TTL, LRU cache) and terminates the sandbox's TLS. The CA private key never leaves the daemon. Leaf keys live in agent memory for the duration of the tunnel and can only impersonate the host they were issued for. A sandbox that dials an upstream IP directly while pinning a non-injected CA can still escape interception; if you need a hard guarantee, run sandboxes inside an egress-restricted network namespace.
- Rules are dropped from the daemon on sandbox destroy. They do not persist across sandboxes.
- The daemon's egress dialer resolves each upstream hostname itself and refuses to dial any IP in the private/internal/link-local/CGNAT/loopback set unless a rule explicitly matched. This defeats DNS-rebinding attacks where a sandbox would otherwise reach cloud metadata (`169.254.169.254`), internal RFC1918 services, or `127.0.0.1` admin endpoints on the daemon host by pointing a hostname at them. Dials are made by IP literal (not hostname) so a re-resolution can't race the check. Operators who want sandboxes to reach private addresses must install an explicit `allow` / `inject` / `defer` rule for the host — there is no implicit allowance.

## Scope

The following are in scope for security reports:

- Sandbox escape (container or microVM breakout)
- Privilege escalation within a sandbox
- Cross-sandbox data access
- Authentication or authorization bypass in multi-tenant mode
- Denial of service against the daemon
- Information disclosure through the API or tunneling layer

## Acknowledgments

We appreciate the security research community and will credit reporters (with permission) in release notes.
