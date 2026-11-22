# Anyserve

Host game servers and other services on any platform from one tool.

Anyserve installs servers from a signed registry of **service definitions**
and runs them as native services or as containers. Run `anyserve` with no
arguments for a guided install, or script everything with flags.

> **Status: early development.** Linux with systemd user services works end
> to end, and the Docker target runs container stacks such as Lemmy. The
> Windows-service, launchd and Kubernetes targets, and native servers inside
> containers, are planned. See [PLAN.md](PLAN.md) for the roadmap and what
> has been tested.

## Install

There are no release binaries yet. Build from source with Go 1.27 or newer:

```bash
go install github.com/jonsjava/anyserve/cmd/anyserve@latest
```

Or clone the repo and run `make build`, which puts the binary in
`bin/anyserve`.

What you need depends on what you host:

- **SteamCMD games** on Linux need 32-bit libraries (on Debian or Ubuntu:
  `sudo apt install lib32gcc-s1`). Anyserve downloads SteamCMD itself.
- **Some games** need a host program, which `anyserve info` lists: `xvfb-run`
  (Core Keeper), the .NET runtime (CryoFall) or 32-bit C libraries (Quake 4).
- **Stacks** such as Lemmy need Docker with the Compose plugin.
- **systemd** user services stop when you log out unless lingering is on:
  `sudo loginctl enable-linger $USER`.

## Quick start

```bash
anyserve                      # guided install
anyserve available            # what you can install
anyserve info terraria        # settings, ports, console and notes
anyserve install terraria --name friends --set max_players=16 --start
anyserve list
anyserve logs friends -f
anyserve console friends      # live console; Ctrl-D to leave
anyserve ports friends        # what to forward on your router
```

## Services

| Service | Source | Runs on | LinuxGSM |
|---|---|---|---|
| American Truck Simulator | SteamCMD | Linux | `atsserver` |
| Core Keeper | SteamCMD | Linux (needs xvfb) | `ckserver` |
| Counter-Strike 2 | SteamCMD | Linux, Windows | `cs2server` |
| CryoFall | SteamCMD | Linux, Windows (needs .NET) | not in LinuxGSM |
| Don't Starve Together | SteamCMD | Linux (surface shard only) | `dstserver` |
| Killing Floor | SteamCMD, Steam account needed | Linux | `kfserver` |
| Lemmy | Container stack | Docker | not a game |
| No One Survived | SteamCMD | Windows | not in LinuxGSM |
| Plains of Pain | SteamCMD | Linux | not in LinuxGSM |
| Quake 4 | LinuxGSM's archive (bring your own base game data) | Linux | `q4server` |
| Rust | SteamCMD | Linux, Windows | `rustserver` |
| Stationeers | SteamCMD | Linux, Windows | `stserver` |
| Terraria | Official archive | Linux, Windows, macOS | `terrariaserver` |
| Wurm Unlimited | SteamCMD | Linux, Windows | not in LinuxGSM |

How far each one has been tested is recorded in [PLAN.md](PLAN.md). Each
service's quirks are noted in its definition and shown by `anyserve info`.

## Commands

| Command | What it does |
|---|---|
| `anyserve` | Guided install: pick a service, target, settings and ports |
| `available [search]` | List installable services |
| `info <service>` | Show a service's settings, ports, console and notes |
| `install <service>` | Install a new instance (`--name`, `--set key=value`, `--port name=N`, `--target`, `--dir`, `--start`) |
| `list` | List instances and their state |
| `status <name>` | Details for one instance |
| `start` / `stop` / `restart <name>` | Control an instance. `restart --warn 5m` warns players first |
| `logs <name>` | Server output (`-f` to follow, `-n` lines) |
| `update <name>` | Re-download the server and apply the newest definition, restarting if it was running (`--warn 10m`) |
| `config <name>` | Show settings (`--show-secrets` for passwords), or change them with `--set` / `--port` |
| `ports <name>` | Which ports to forward, with firewall examples |
| `console <name> [cmd]` | Run one console command and see the reply, or open a live console. Uses RCON when the server has it (`--stdin` to skip). `--info` shows how to connect from other tools |
| `rcon <host:port> [cmd]` | Talk to any server's RCON (`--protocol source` or `webrcon`), on this machine or another |
| `say <name> <msg>` | Broadcast a chat message |
| `backup` / `backups` / `restore` | Back up an instance, list backups, restore one (a safety backup is taken first) |
| `remove <name>` | Stop and delete an instance. Backups are kept |
| `steam login <user>` / `steam logout` | Sign in through SteamCMD for servers that need an account. Anyserve stores only the user name |
| `refresh` | Download the latest definitions from every registry |
| `registry list/add/remove` | Manage extra registries |
| `targets` | List where services can run |
| `version` | Show the version |

Run `anyserve <command> --help` for details and examples.

## How it works

- **Service definitions** (`registry/services/*.yaml`) describe where a
  service comes from (SteamCMD, an HTTPS archive pinned by checksum, or
  container images pinned by digest), its settings and ports, its config
  files and how to run, stop and talk to it. Definitions are **declarative
  only**: there are no shell hooks, every path must stay inside the
  instance's folder, and unknown fields are rejected. The full format is in
  [docs/definitions.md](docs/definitions.md).
- **The registry index** is built from those files and signed with Ed25519.
  Anyserve only uses an index whose signature matches a pinned key. Each
  binary also carries a signed snapshot, so it works offline. Until the
  public registry is published, the built-in snapshot is the main registry,
  and `anyserve refresh` says so.
- **Targets** decide where an instance runs:
  - `systemd` (Linux) runs `anyserve run <name>` as a systemd *user*
    service. That command supervises the server: it gives the server a
    console socket only your user can use, logs its output, and stops it by
    typing its own stop command (so the world saves) before falling back to
    signals.
  - `docker` runs stacks through Docker Compose, from a Compose file Anyserve
    writes itself. Images are pinned by digest, containers run as your user
    with all capabilities dropped, and they can only mount folders inside
    the instance.
  - `windows-service`, `launchd` and `kubernetes` are planned and listed by
    `anyserve targets`.
- **Consoles.** `anyserve console` reaches a server through RCON when its
  definition declares it (Source RCON or Rust's WebRCON), otherwise through
  the server's own console. A stack can name a console program, such as
  Lemmy's database shell. RCON ports stay private by default; use an SSH
  tunnel to reach them from another computer.
- **Nothing runs as root.** Servers run as your user, and each one gets its
  own `HOME` (its server folder), so per-user game files stay with the
  instance.

## Where files live

| | Linux | macOS | Windows |
|---|---|---|---|
| Instances, backups, SteamCMD | `~/.local/share/anyserve` | `~/Library/Application Support/anyserve` | `%LOCALAPPDATA%\anyserve` |
| Config | `~/.config/anyserve` | `~/Library/Application Support/anyserve` | `%APPDATA%\anyserve` |
| Registry cache, downloads | `~/.cache/anyserve` | `~/Library/Caches/anyserve` | `%LOCALAPPDATA%\anyserve` |

Set `ANYSERVE_HOME` to keep everything under one folder instead, which is
handy for testing.

## Contributing

New service definitions are the most useful contribution. See
[CONTRIBUTING.md](CONTRIBUTING.md) for how to write and test one, and
[docs/definitions.md](docs/definitions.md) for the format. Security
reports go through [SECURITY.md](SECURITY.md).

## Development

```bash
make build       # bin/anyserve
make test        # vet and quick tests
make test-full   # all tests with the race detector
make check       # validate every definition
make registry    # rebuild the signed snapshot (needs the signing key)
make release     # cross-compile for Linux, Windows and macOS into dist/
```

The registry signing key isn't in the repo. `make registry` reads it from
`~/.config/anyserve-dev/registry-signing.key` (override with `KEY=...`); its
public key is pinned in `internal/registry/defaults.go`. Contributors don't
need it: `make check` validates definitions without signing.

## Credits

Several definitions were translated from the
[Pelican eggs](https://github.com/pelican-eggs) (MIT) and
[LinuxGSM](https://github.com/GameServerManagers/LinuxGSM) (MIT), including
fixes LinuxGSM worked out for particular games. Lemmy's stack follows the
container layout of [lemmy-ansible](https://github.com/LemmyNet/lemmy-ansible).
Game and server software is downloaded at install time, from the source each
definition names, and never redistributed here.

## License

[MIT](LICENSE)
