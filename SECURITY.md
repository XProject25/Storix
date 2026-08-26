# Security policy

Storix reads and writes files on a server, so security reports are taken
seriously and handled quickly.

Developed by X Project.

## Reporting a vulnerability

Please report privately through
[GitHub security advisories](https://github.com/XProject25/Storix/security/advisories/new)
rather than opening a public issue.

Include what you can:

- the version, from `storix version`
- the platform and how Storix was installed
- what an attacker gains, and what access they need first
- steps to reproduce, or a proof of concept

You can expect an acknowledgement within a few days, an assessment with a fix
plan after that, and credit in the release notes when the fix ships, unless you
prefer otherwise.

## Supported versions

The newest 1.x release receives security fixes.

## What is in scope

- Escaping the folders an account was granted, including through symlinks,
  archive extraction or upload paths
- Reaching a protected location such as `/etc/shadow` or `/root/.ssh`
- Authentication or session flaws, privilege escalation between roles
- Public share links exposing more than they should
- Stored content executing in the Storix origin

## What is not in scope

- An administrator granting an account access to a sensitive folder on purpose.
  Administrators can reach every mounted folder by design.
- Installing with `--user root` and then reaching files that root can reach.
- Denial of service through legitimately expensive operations, for example a
  recursive size calculation over millions of files.
- Anything requiring shell access to the server, which already implies control
  of the machine.

## How Storix defends itself

- Every path resolves against the mounts the account owns, and file access runs
  through `os.Root`, which pins operations below that directory in the kernel.
  Symlink escapes and check then open races are closed by construction.
- Protected locations are refused even inside a mounted parent.
- Argon2id password hashing, session identifiers stored as hashes, CSRF double
  submit tokens, login rate limiting with account lockout, optional TOTP.
- Archive extraction rejects entries whose names point outside the destination,
  absolute paths and escaping symlinks, and guards against decompression bombs.
- Downloads carry `X-Content-Type-Options: nosniff` and a
  `default-src 'none'; sandbox` content policy, so stored HTML cannot execute in
  the application origin.
- The service runs as a dedicated account and the binary stays root owned, so a
  compromised service cannot rewrite the program.

The guarded file system layer is `internal/vfs`, credential handling is
`internal/auth`, and the archive safety checks are in `internal/archive`.
Review is welcome.
