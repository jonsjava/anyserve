# Anyserve

Host game servers and other services on any platform from one tool.

Anyserve installs servers from a signed registry of **service definitions**
and runs them as native services or as containers. Run `anyserve` with no
arguments for a guided install, or script everything with flags.

> **Status: early development.** Linux with systemd user services works end
> to end, and the Docker target runs container stacks such as Lemmy. The
> Windows-service, launchd and Kubernetes targets are planned; native
> servers can already run inside containers (`--isolate`).

## Install

On Linux or macOS, install the latest release for your user (no root needed):

```bash
curl -fsSL https://github.com/jonsjava/anyserve/releases/latest/download/install.sh | sh
```

It puts `anyserve` in `~/.local/bin`, after checking it against the release's
checksums and those against Anyserve's release key. `anyserve upgrade` moves to
a newer release the same way. Windows builds are attached to each release.

Or build from source with Go 1.27 or newer:

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
capability dropped. Their web pages are opened to the network, and settings that
hold the app's own address (such as `url`) are filled in from this machine's
address and port, so an app works from other computers as soon as it's
installed; the install prints where to open it. With a domain, the page goes
through the HTTPS proxy instead (see below). `--private` at install keeps the
pages on this machine only (`127.0.0.1`), and `anyserve config <name>
--expose http` or `--unexpose http` changes it later.

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
| n.eko | Shared Firefox in the browser, streamed over WebRTC | 8085, media 52000 |
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

Each service's quirks are noted in its definition and shown by `anyserve info`.

## Commands

| Command | What it does |
|---|---|
| `anyserve` | Guided install: pick a service, target, settings and ports |
| `available [search]` | List installable services |
| `info <service>` | Show a service's settings, ports, console and notes |
| `install <service>` | Install a new instance (`--name`, `--set key=value`, `--port name=N`, `--private`, `--expose port`, `--target`, `--dir`, `--start`) |
| `list` | List instances and their state |
| `status <name>` | Details for one instance, with uptime, CPU, memory, disk and network while it runs |
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
| `users add/list/grant/revoke/admin/cert/cert-revoke` | Who may manage instances remotely. See [Remote access](#remote-access) |
| `https use/status/sync` | The HTTPS proxy for apps' domains: Caddy (default, run by Anyserve), your own nginx or Traefik, or none. See [HTTPS for apps](#https-for-apps) |
| `remote enable/disable/status` | This host's built-in SSH server for remote users |
| `remote add/list/remove` | Hosts you manage from this computer |
| `reserve add/list/remove` | Keep ports free for things Anyserve doesn't run, e.g. another panel's stopped servers |
| `fleet token/join/list/remove/leave` | Manage several machines from one. See [More machines](#more-machines) |
| `upgrade [--check]` | Update Anyserve itself to the latest signed release |
| `proxy [host]` | The web UI on your own 127.0.0.1, for this machine or a remote host |
| `port-forward <host> <instance> <port>` | Reach a remote instance's TCP port on this computer |
| `serve` | The API on a private socket, plus the SSH server when remote access is on (installed as a service by `remote enable`) |
| `auto-update set/run/status/enable/disable/notify` | Scheduled updates: per instance `off`, `notify` (default) or `apply`. See [Scheduled updates](#scheduled-updates) |
| `steam login <user>` / `steam logout` | Sign in through SteamCMD for servers that need an account. Anyserve stores only the user name |
| `refresh` | Download the latest definitions from every registry |
| `registry list/add/remove` | Manage extra registries |
| `targets` | List where services can run |
| `version` | Show the version |

Run `anyserve <command> --help` for details and examples.

## Scheduled updates

```bash
anyserve auto-update enable                 # a systemd user timer, 04:00 daily by default (--at)
anyserve auto-update set friends apply      # this instance updates itself
anyserve auto-update notify https://ntfy.sh/your-private-topic
anyserve auto-update status
```

Each run refreshes the registry and checks every instance for a changed
definition (a new version, or new image digests) and, for Steam servers, a
newer Steam build. What happens next depends on the instance's policy:

- `off`: not checked.
- `notify` (the default): recorded in `list`, `status` and `auto-update status`, and posted to the notify URL.
- `apply`: backed up, updated and then watched. If the server isn't healthy within
  10 minutes (`--health-limit`), or doesn't stay healthy for a minute (`--settle`),
  the update is rolled back: the old definition and the pre-update backup are put
  back and the update is held, so later runs skip it. `anyserve update <name>`
  tries a held update anyway.

For apps, a rollback is complete: the old definition names the old image digests.
For native servers, only the data comes back; the server's program files stay at
the new version.

## HTTPS for apps

```bash
anyserve install vaultwarden --domain vault.example.com
anyserve config gotify --domain push.example.com     # later, or --no-domain
anyserve https use caddy --email you@example.com     # optional: Let's Encrypt notices
```

The port the domain serves listens on 127.0.0.1, and the app's address settings
use `https://` and the domain. A domain puts the app behind an HTTPS reverse
proxy with a free Let's Encrypt certificate. By default that's Caddy, which
Anyserve runs for you: one container, pinned by digest, running as your user with
nothing but permission to use ports 80 and 443, and only while some app has a
domain. The domain's DNS must point at this machine, and ports 80 and 443 must
reach it.

Already run a proxy? `anyserve https use nginx` or `anyserve https use traefik`
writes a config file for it instead (`anyserve https status` shows where) and
keeps it up to date as domains change. `anyserve https use none` turns domains off.

### Domains for games and other servers

Any instance can have a domain; what it does depends on how the server talks:

- **Minecraft (Java):** every server with a domain shares the standard port.
  Players type `play.example.com` or `creative.example.com` and Anyserve's
  router (mc-router, pinned, running as your user with no privileges) sends them
  to the right server. Each server moves to a port of its own on this machine
  automatically; only the router's port (25565, or `anyserve router port <n>`)
  needs forwarding. `anyserve router` lists the routes.
- **Servers that do their own TLS** (definitions mark the port `routing: tls`):
  they share port 443 with the apps. Caddy reads the name in the TLS handshake
  and passes the connection through without decrypting it. Anyserve builds this
  Caddy (built from source with caddy-l4, pinned) on the first such domain; it needs the Caddy
  provider.
- **Everything else** (most games, especially UDP ones) can't be routed by name,
  so the domain is just a DNS name for this machine and its ports still need
  forwarding.

`anyserve dns <name>` prints the records to create: an A/AAAA record (this
machine's public addresses, read locally, or a placeholder behind NAT) and SRV
records for games that look them up, so players can leave the port off.

```bash
anyserve install minecraft --name survival --domain play.example.com
anyserve dns survival
```

## Remote access

Share a host with friends without exposing a web panel to the internet. It works
like `kubectl proxy`: Anyserve runs its own SSH server (keys only, no shell, no
root, no OS accounts), and each person's web UI and forwarded ports open on
their own computer's 127.0.0.1.

On the host:

```bash
anyserve users add alice --key alice.pub
anyserve users grant alice friends-rust          # alice sees and manages only this one
anyserve users add sam --key sam.pub --admin
anyserve users cert sam                          # sam's browser certificate (sam.p12)
anyserve remote enable --listen 0.0.0.0:7022     # open TCP 7022 in your firewall
```

On alice's computer:

```bash
anyserve remote add box alice@box.example.com:7022 --key ~/.ssh/id_ed25519
anyserve proxy box                               # the web UI, on https://127.0.0.1:7080
anyserve port-forward box friends-rust rcon      # e.g. for an RCON tool
sftp -P 7022 alice@box.example.com               # the files of alice's instances
```

- **Users** see and manage only the instances granted to them: start, stop, restart, update,
  back up, logs, console and files.
- **Admins** see everything, but only in the web UI with their browser client certificate,
  which is checked on the host. Without it they're ordinary users. With it, the web UI also
  manages users (add, make admin, give servers, add keys, issue certificates, remove) and
  servers (install from the catalog, with free ports filled in, and remove).
- **Live stats** on each server's Overview: uptime, CPU (100% is one core), memory, disk and
  network, with two-minute graphs, like a game panel's. Native servers share the machine's network,
  so their traffic shows only once they're isolated (`--isolate`).
- **The file manager** edits text files in the browser and uploads, downloads, renames, deletes,
  archives (.zip or .tar.gz) and unpacks archives. Everything stays inside the instance's folder:
  links can't lead out of it, and unpacking skips links and stops at 10 GiB.
- **Installs pick free ports:** a default port that another instance or program already uses moves
  to the next free number, in the CLI and the web UI alike.
- **Reserved ports** are skipped too. Reserve ports something else owns but isn't always listening
  on, such as another panel's stopped servers: `anyserve reserve add 27015-27030 --note Pterodactyl`
  (`--protocol tcp|udp`, default both), or the web UI's **Ports** page, per machine. Picking a
  reserved port yourself still works, with a warning.
- **The host's web CA only covers localhost addresses**, so trusting it in your browser can't
  vouch for any real website.
- **On the host itself**, `anyserve proxy` (no host) opens the same UI with full access.
- **Everything remote users change is logged** to `audit.log` in the data folder.

### More machines

One machine (the manager) can manage others: their servers show up in its web UI
under a machine picker at the top, with the same install, file manager, logs and
console. Workers don't need an open port: each one dials the manager's SSH port
and keeps that connection open.

On the manager, with remote access on (`anyserve remote enable --listen 0.0.0.0:7022`),
open **Machines → Get the command** in the web UI, or run:

```bash
anyserve fleet token
```

It prints one command to run on the new machine, as the user that should run its servers:

```bash
curl -fsSL https://github.com/jonsjava/anyserve/releases/latest/download/install.sh | sh -s -- fleet join 203.0.113.5:7022 <token>
```

That installs Anyserve, joins, and starts it as a service.

- **The token works once, for an hour**. It also carries the manager's key fingerprint, so the
  new machine can't be tricked into joining an impostor.
- **The manager's address** comes from remote access's listen address, and its public address is
  filled in when that's 0.0.0.0. Set it with `anyserve fleet token --addr host:port` (it's
  remembered) when workers reach it another way, e.g. through a VPN or a DNS name.
- **`anyserve fleet list`** shows each machine and whether it's connected. **`anyserve fleet remove
  <name>`** (or **Remove** in the web UI) cuts one off. Its servers keep running, but you can't
  manage them from the manager any more. On the worker, `anyserve fleet leave` forgets the manager.
- **Only admins** (and the manager's owner) can reach other machines. A worker trusts the manager
  as its owner, so treat the manager like the most sensitive machine in the fleet.
- **`--no-service`** joins without installing the service, e.g. in a container. Keep
  `anyserve serve` running there yourself.

### Isolating native servers

`anyserve install <game> --isolate` (or `anyserve config <name> --isolate` later)
runs a native game server in a container that sees only its own server folder.
It runs as your user, with no capabilities, so instances can't read each other's
files or processes. The image is the definition's `container` hints (a base
image plus packages), built locally once. Server files stay mounted, never baked
in. Consoles, logs, backups and clean stops work as before, because Anyserve's
supervisor stays outside the container. This needs Linux and Docker, and works
for definitions with `container` hints.

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
