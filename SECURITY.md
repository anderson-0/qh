# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub:
[Report a vulnerability](https://github.com/anderson-0/qh/security/advisories/new) (the repository's
Security tab → Report a vulnerability). Do not open a public issue for a security problem.

Include what you found, how to reproduce it, and the qh version (`qh --version`). You will get an answer
within a week.

## Supported versions

Fixes go into the latest release. qh follows semantic versioning, so updating within version 1 is safe.

## Scope

qh's server mode (`qh serve --http`) is designed for tenants who may be hostile: they supply connection
strings, SSH keys and queries. Ways for a tenant to read files on the qh host, use its credentials, reach
addresses the network policy refuses, see another workspace's data, or make qh use excessive CPU or memory
are in scope. So are secrets (passwords, keys) appearing in errors, logs or agent-visible output.
