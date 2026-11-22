# Security

Anyserve downloads and runs server software, so the registry it installs
from is a supply-chain target. This page explains the protections and how to
report a problem.

## Reporting a vulnerability

Please report security problems privately through GitHub: on this
repository's **Security** tab, choose **Report a vulnerability**. Don't open
a public issue for them.

Include what you found, how to reproduce it, and what an attacker could do
with it. This is a hobby project, so there's no fixed response time, but
security reports come first.

## What Anyserve protects against

- **Tampered definitions.** The registry index is signed with Ed25519, and
  Anyserve refuses an index whose signature doesn't match the pinned key.
  Each binary also carries a signed snapshot. Extra registries must name
  their own public key.
- **Definitions that run code.** Definitions are data. They can't contain
  shell commands. Host actions are limited to built-in step types, servers
  start only from files inside their own folder, and host launchers come from
  a fixed list (`xvfb`, `dotnet`, `java`, `mono`).
- **Path escapes.** Every path in a definition is relative and checked to
  stay inside the instance's folder, including through symlinks. Archive
  entries that would escape are rejected, and symlinks in archives are
  skipped.
- **Swapped downloads.** Downloads are HTTPS only. Archives and extra files
  are checked against pinned SHA-256 sums, and container images must be
  pinned by digest. SteamCMD content is verified by Steam itself.
- **Over-privileged containers.** Stack containers run as the installing
  user with all Linux capabilities dropped and `no-new-privileges` set.
  Definitions can't ask for privileged mode, the host network, host paths
  or the Docker socket. Ports that aren't public bind to 127.0.0.1.
- **Secrets.** Config files and instance records are written with mode 0600.
  Passwords are hidden in output unless you pass `--show-secrets`. The
  server console socket is only usable by your user. Steam passwords are
  never seen or stored: SteamCMD handles the sign-in and keeps the session.
  `anyserve rcon` reads passwords from the environment or a prompt, not from
  the command line.

## What it doesn't protect against

- **The servers themselves.** Game servers and container images run their
  own code. Anyserve limits where they can write and, for containers, what
  they can do, but a malicious or buggy server can still misbehave within
  those limits. Install services you trust.
- **Docker group access.** Being able to use Docker is close to being root.
  Anyserve's stacks don't need more than normal containers, but the access
  itself is yours to manage.
- **Exposed admin ports.** RCON and web admin ports are private by default.
  If you forward them to the internet, use strong passwords, or better, an
  SSH tunnel or VPN.
- **Unpinned sources.** SteamCMD's bootstrap download from Valve isn't
  checksummed, because Valve doesn't publish sums (SteamCMD then updates
  itself). Archive definitions without `sha256` are allowed but discouraged.
