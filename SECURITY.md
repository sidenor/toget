# Security Policy

Toget is pre-release software and has no supported versions yet. Do not use it for production or for actions that could cause financial loss, data loss, or unwanted external effects.

## Reporting a vulnerability

Please report security issues privately through GitHub's **Report a vulnerability** feature on the repository's Security tab (GitHub Security Advisories). Do not open a public issue.

If private reporting is unavailable, contact the maintainer through the GitHub profile listed on the repository.

Please include:

- A description of the issue and its impact
- Steps to reproduce, or a proof of concept
- Affected versions or commit hashes
- Any suggested mitigation

We aim to acknowledge reports within three business days and will keep you informed about remediation. Please give us a reasonable amount of time to fix the issue before publishing details.

## Scope

Especially relevant areas:

- Authentication and authorization between users, agents, and rooms
- Local daemon pairing and token handling
- Prompt injection that can cause unintended tool execution
- Approval bypasses and audit-log tampering
- Secret storage and leakage
- Path traversal, SSRF, and command injection in connectors

## Out of scope

- Issues that require physical access to an unlocked device
- Social engineering
- Denial of service through unrealistic resource consumption
- Vulnerabilities in third-party services or models that Toget merely calls

## Safe harbor

We will not pursue legal action against researchers who report issues in good faith, avoid privacy violations and data destruction, and give us time to respond before disclosure.
