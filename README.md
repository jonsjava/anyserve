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

### Game servers

These run as native programs, supervised by systemd on Linux (or a scheduled
task on Windows), or in a container with `--target docker`.

| Game | Source | Runs on | LinuxGSM |
|---|---|---|---|
| American Truck Simulator | SteamCMD | Linux | `atsserver` |
| Arma Reforger | SteamCMD | Linux, Windows | `armarserver` |
| Core Keeper | SteamCMD | Linux (needs xvfb) | `ckserver` |
| Counter-Strike 2 | SteamCMD | Linux, Windows | `cs2server` |
| CryoFall | SteamCMD | Linux, Windows (needs .NET) | not in LinuxGSM |
| Don't Starve Together | SteamCMD | Linux (surface shard only) | `dstserver` |
| Factorio | Official archive | Linux | `fctrserver` |
| Garry's Mod | SteamCMD | Linux, Windows | `gmodserver` |
| Killing Floor | SteamCMD, Steam account needed | Linux | `kfserver` |
| Mindustry | Official release | Linux, Windows, macOS (needs Java 17+) | `mindustryserver` |
| Minecraft (Java Edition) | Mojang's server jar | Linux, Windows, macOS (needs Java 25) | `mcserver` |
| Minecraft (Paper) | PaperMC's server jar | Linux, Windows, macOS (needs Java 25) | `pmcserver` |
| Minecraft (Bedrock Edition) | Mojang's archive | Linux | `mcbserver` |
| Necesse | SteamCMD | Linux, Windows | `necserver` |
| No One Survived | SteamCMD | Windows | not in LinuxGSM |
| Palworld | SteamCMD | Linux, Windows | `pwserver` |
| Plains of Pain | SteamCMD | Linux | not in LinuxGSM |
| Project Zomboid | SteamCMD | Linux, Windows | `pzserver` |
| Quake 4 | LinuxGSM's archive (bring your own base game data) | Linux | `q4server` |
| Rust | SteamCMD | Linux, Windows | `rustserver` |
| Satisfactory | SteamCMD | Linux, Windows | `sfserver` |
| Stationeers | SteamCMD | Linux, Windows | `stserver` |
| Terraria | Official archive | Linux, Windows, macOS | `terrariaserver` |
| Unturned | SteamCMD | Linux, Windows | `untserver` |
| Valheim | SteamCMD | Linux, Windows | `vhserver` |
| Wurm Unlimited | SteamCMD | Linux, Windows | not in LinuxGSM |

Minecraft's servers need you to accept Mojang's EULA: Anyserve asks, and
won't install until you add `--set accept_eula=true` yourself.

### Self-hosted apps

These run as container stacks (Docker Compose) under your own user, with every
capability dropped. Their web pages listen on this machine only
(`127.0.0.1`); put them behind your reverse proxy, or open one to your network
with `--expose http` at install or `anyserve config <name> --expose http` later.

| App | What it is | Default port |
|---|---|---|
| Actual Budget | Personal budgeting | 5006 |
| Audiobookshelf | Audiobook and podcast server (shares a folder you choose) | 13378 |
| Forgejo | Git hosting (rootless) | 3003, SSH 2223 |
| Gitea | Git hosting (rootless) | 3000, SSH 2222 |
| Gotify | Push notifications | 8070 |
| Grafana | Dashboards | 3002 |
| Harmony | Federated Discord-style chat, with its Supabase backend | 8090, API 8091 |
| Immich | Photo and video library | 2283 |
| Jellyfin | Media server (shares a folder you choose) | 8096 |
| Lemmy | Link aggregator for the fediverse | 10633 |
| Mealie | Recipes and meal plans | 9925 |
| Memos | Notes | 5230 |
| Miniflux | Feed reader | 8085 |
| Mumble | Voice chat server | 64738 |
| n8n | Workflow automation | 5678 |
| Navidrome | Music streaming (shares a folder you choose) | 4533 |
| ntfy | Push notifications | 2586 |
| Paperless-ngx | Document archive with OCR | 8000 |
| Syncthing | File sync (shares a folder you choose) | 8384, sync 22000 |
| Umami | Website analytics | 3004 |
| Uptime Kuma | Uptime monitoring | 3001 |
| Vaultwarden | Bitwarden-compatible password manager | 8222 |

Apps that share your files take a folder setting, e.g.
`--set media_folder=~/Videos`. Anyserve refuses system folders, your whole home
folder and folders holding keys (such as `~/.ssh`).

How far each one has been tested is recorded in [PLAN.md](PLAN.md). Each
service's quirks are noted in its definition and shown by `anyserve info`.

## Commands

| Command | What it does |
|---|---|
| `anyserve` | Guided install: pick a service, target, settings and ports |
| `available [search]` | List installable services |
| `info <service>` | Show a service's settings, ports, console and notes |
| `install <service>` | Install a new instance (`--name`, `--set key=value`, `--port name=N`, `--expose port`, `--target`, `--dir`, `--start`) |
| `list` | List instances and their state |
| `status <name>` | Details for one instance |
| `start` / `stop` / `restart <name>` | Control an instance. `restart --warn 5m` warns players first |
| `logs <name>` | Server output (`-f` to follow, `-n` lines) |
| `update <name>` | Re-download the server and apply the newest definition, restarting if it was running (`--warn 10m`) |
| `config <name>` | Show settings (`--show-secrets` for passwords), or change them with `--set` / `--port`. `--expose` / `--unexpose` open an app's port to your network or close it again |
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
  binary also carries a signed snapshot, so it works offline. The main
  registry is published from
  [anyserve-registry](https://github.com/jonsjava/anyserve-registry) and
  `anyserve refresh` downloads its latest index.
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
make publish-registry  # sign and publish by hand (the workflow normally does this)
make release     # cross-compile for Linux, Windows and macOS into dist/
```

Two GitHub Actions workflows run the project:

- **CI** (`.github/workflows/ci.yml`) checks every pull request: formatting,
  definitions, tests with the race detector, and builds for each platform.
  It has no secrets, so pull requests from forks are safe to run.
- **Publish registry** (`.github/workflows/publish-registry.yml`) runs when
  definition changes reach `main`. It signs a new index, pushes it to
  [anyserve-registry](https://github.com/jonsjava/anyserve-registry) for
  GitHub Pages, and commits the same index back as the built-in snapshot.

The signing key isn't in the repo. It's a secret in the `registry`
environment, which only `main` can use, and the public key is pinned in
`internal/registry/defaults.go`. To sign locally instead, `make registry`
reads it from `~/.config/anyserve-dev/registry-signing.key` (override with
`KEY=...`). Contributors don't need it: `make check` validates definitions
without signing.

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
