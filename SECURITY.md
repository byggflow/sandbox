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

- Container hardening (Docker runtime)
- microVM isolation (Firecracker runtime)
- Network isolation
- Access control and multi-tenant identity scoping
- End-to-end encryption

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
