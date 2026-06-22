# Security Policy

## Supported versions

verix-dbm is pre-1.0; security fixes land on the latest release and `main`.
Please reproduce on a recent version before reporting.

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/KlevjanPrifti/verix-dbm/security/advisories).
Include the affected version, impact, and a proof of concept if you have one.
Do not open a public issue for security reports.

## Security model

For how verix-dbm is hardened (SSO, RBAC, encrypted credentials at rest, CSRF,
the SSRF egress guard, and the audit log), see the
[security documentation](https://klevjanprifti.github.io/verix-dbm/security).
