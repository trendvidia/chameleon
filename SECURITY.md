# Security Policy

chameleon decrypts configuration secrets and holds them in memory for the
life of a process, so we treat security reports as the highest-priority
kind of bug.

## Reporting a Vulnerability

Please report vulnerabilities privately. **Do not open a public GitHub
issue or pull request** for security-sensitive reports.

Preferred channels:

1. **GitHub Private Vulnerability Reporting** — open a report at
   <https://github.com/trendvidia/chameleon/security/advisories/new>.
   This routes directly to the maintainers and keeps the disclosure private
   until a fix is published.
2. **Email** — `security@trendvidia.com`. PGP key available on request.

Please include:

- A description of the issue and the impact you observed.
- Steps to reproduce, ideally a minimal test case or layer file.
- The module and release tag the report applies to (`v0.25.1`,
  `sops/v0.25.1`, `cmd/chameleon/v0.25.1`, or a commit).
- Whether you intend to publish a writeup, and on what timeline.

## What to Expect

- Acknowledgement within **3 business days**.
- An initial assessment (severity, scope, affected versions) within **7
  business days**.
- Status updates at least every 14 days until the report is closed.
- Credit in the release notes and any published advisory, unless you ask
  to remain anonymous.

## Scope

In scope, across all three modules in this repository (the library, the
`sops` backend and the `chameleon` CLI):

- Secret plaintext escaping its memguard enclave: reachable as a plain
  `string` on a resolved config, left in unlocked or unwiped memory,
  written to logs, errors, traces, metrics or CLI output (`explain`,
  `get` and friends redact by design — a bypass is a bug).
- Decryption that succeeds when it should fail: an encrypted layer
  accepted without authentication, a tampered layer or bundle accepted,
  a `@header.content_sha256` check that can be sidestepped.
- Panics, unbounded memory or path traversal on attacker-controlled
  layer files, chain specs or bundles.
- The CLI's key handling (`rotate-key`, `migrate`, the key store) writing
  key material with loose permissions or leaving it behind.

Out of scope:

- Vulnerabilities in third-party dependencies — please report those
  upstream (sops issues in the fork go to
  [trendvidia/sops](https://github.com/trendvidia/sops)). We will bump
  dependency versions in response.
- Attacks that need a hostile *trusted* input: the `.proto` schema and
  the chain spec are part of the operator's trust boundary, as are the
  decryption keys themselves.
- An attacker who can already read the process's memory as root, or
  attach a debugger to it. memguard raises the cost of that; it does not
  prevent it.

## Supported Versions

Until `v1.0.0`, only the latest minor release receives security fixes.
After `v1.0.0`, the policy will be revisited and documented here.

## Coordinated Disclosure

We follow a 90-day coordinated disclosure window by default. If a fix
ships sooner, the advisory will be published immediately. Reports
involving an actively exploited vulnerability will be triaged
out-of-band — flag this in your initial email.
