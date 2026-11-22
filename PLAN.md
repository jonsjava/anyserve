# Anyserve: plan

Last updated 2026-11-22. Anyserve is a successor to the author's older `wurm_unlimited_server` scripts. This file is the working plan and test log; for how to use Anyserve, see the [README](README.md).

## What it is

A tool for hosting servers and services on any platform without being tied to one. One service definition runs on Linux, Windows or macOS, on bare metal, a VM, in containers or on Kubernetes. Game servers are the first content, but the model isn't limited to games.

It's a hobby project, built for me first. It isn't trying to beat LinuxGSM or Pterodactyl. The goal is to improve things, and every install also gets ported to LinuxGSM.

## Decisions so far

| Area | Decision |
|---|---|
| Name | **Anyserve** (CLI: `anyserve`, with `anysrv` as a possible short alias) |
| Positioning | One simple model for running servers (B), with games the big tools handle badly as the launch content (A) |
| Language | Go |
| Release builds | Linux, Windows and macOS |
| Deployment targets | Bare metal or VM, containers, Kubernetes |
| Launch services | Rust, Terraria, Wurm Unlimited |
| Windows | In scope from the start |
| Upstream | Port every install to LinuxGSM |
| Audience | Me first |

### Name check (2026-11-22)
- **USPTO:** no marks for `anyserve`, `anyserv` or `anyserver`.
- **WIPO Global Brand Database** (90 offices, including the EU): two Korean marks, both expired. They are ANYSERVE (Class 42, expired 2022) and ANYSERVER (Class 9, expired 2011).
- **UK IPO:** not searched directly because its site required a CAPTCHA. WIPO's data should include UK records.
- **GitHub and the web:** only small unrelated projects. The nearest is a Python package called `anyserver`.
- This is a database search, not legal clearance.

## The central registry

- One git repo of **service definitions**, modelled on Scoop buckets, Homebrew taps and winget-pkgs.
- **Contributions** come in as pull requests. CI checks every definition against a schema and runs a test install where possible.
- **The published index** is built from the repo and served as a static `index.json`, for example from GitHub Pages. The CLI downloads it and caches it for offline use.
- **The index is signed**, and the CLI verifies it against a pinned key. This builds on idea #7, the verified install-script runner.
- **Extra registries:** users can add their own, for private or experimental services.
- **Definitions are declarative only:** a download source, ports, config templates and a health check. No arbitrary shell hooks. If a service needs a custom step, it becomes a named step type in the Go code. This keeps the registry from becoming a way to run any code on users' machines.

## Guided and scripted installs

- **Run `anyserve` with no arguments and it starts a wizard:** choose a service from the registry index, a target, the install location, ports, service settings and then confirm. Candidate libraries are charmbracelet `huh` and `bubbletea`.
- **With flags it runs non-interactively**, for scripts and CI.
- **The wizard ends by printing the equivalent command** and saving the answers to a config file, so a server can be rebuilt exactly.

## Architecture: definitions and targets

A **service definition** describes the service. A **target** turns that into a running server.

| Target | Linux | Windows | macOS |
|---|---|---|---|
| Bare metal or VM | systemd service | Windows service | launchd |
| Container | Docker or Podman | Docker Desktop (Linux containers) | Docker Desktop |
| Kubernetes | Generates YAML | Generates YAML | Generates YAML |

- **Kubernetes:** start by generating manifests (a StatefulSet, a PersistentVolumeClaim and a UDP Service) rather than managing the cluster directly. Make the output compatible with Agones later.
- **Supported platforms per definition:** each definition lists where it runs natively, so the tool can say "no native server on macOS, use the container target."
- **macOS:** as far as I know, Rust and Wurm Unlimited have no native macOS server, and Terraria does. Rust in a container on Apple Silicon runs under x86 emulation, which is fine for testing but probably too slow to play on.
- **Security:** run each server as an unprivileged user or in a rootless container, and never need root after setup.

## Day-one features

- Install, start, stop, update and remove a server
- Back up and restore, with scheduled backups and a retention limit
- Detect updates and restart cleanly, with a warning to players where the game supports it
- Open ports, or say exactly which ones to forward
- Handle mods where they matter (Wurm, Rust, Terraria)
- The central registry and the guided wizard

## Why these three services make a good proof of concept

- **Rust:** a SteamCMD install (app 258550), regular forced updates, mod frameworks (Oxide/uMod, Carbon) and a heavy server. It tests updates and resource handling.
- **Terraria:** a plain zip download from terraria.org, not Steam, plus the TShock modding layer. It tests non-Steam sources and text config files, and it's the one with a native macOS server.
- **Wurm Unlimited:** SteamCMD again, but the server setup is built around a GUI, so running it headless takes the most work. The old `wurm_unlimited_server` repo (shell scripts plus Chef cookbooks, last updated January 2018) has the earlier know-how.

## Porting to LinuxGSM

- As far as I know, LinuxGSM already has Rust (`rustserver`) and Terraria (`terrariaserver`). For those, contribute fixes and improvements upstream.
- I don't think it has Wurm Unlimited. If that's right, WU is the first new game to contribute.
- LinuxGSM writes each game as a hand-written shell module, so ports are manual pull requests. Each service definition records its LinuxGSM status.

## Milestones

1. **Service definition schema and the registry repo** with all three services, CI validation and a signed index.
2. **The CLI working end to end on the first platform:** install, start, stop, update, back up and remove, for all three services.
3. **The guided wizard.**
4. **The remaining native platforms:** Windows services and macOS launchd.
5. **The container target.**
6. **The Kubernetes target** (generating YAML).
7. **Scheduled backups, update checks and player warnings.**

Send LinuxGSM pull requests alongside, starting once the Wurm Unlimited headless setup works.

## Risks

- **Ongoing maintenance:** games update and break installers, so keep the list of supported services short.
- **Licensing:** some servers can't be redistributed. Always download them from SteamCMD or the official source at install time, and never bundle them.
- **Registry security:** a registry that installs software is a supply-chain target. That's why definitions are declarative and the index is signed.

## Progress

### Session 1 (2026-11-22)
- **Built:** the Go CLI (`cmd/anyserve`), the definition schema and validation (`internal/spec`), the signed registry with an embedded snapshot (`internal/registry`, `tools/regbuild`), HTTPS archive and SteamCMD downloads (`internal/fetch`), built-in install steps (`internal/steps`), the process supervisor with console socket and clean stop (`internal/supervise`), the systemd user-service target (`internal/target`), backups and restore, and the guided wizard (`internal/cli/wizard.go`).
- **Definitions:** Rust, Terraria (1.4.5.8, checksum pinned) and Wurm Unlimited, in `registry/services/`.
- **Verified end to end on Linux with Terraria:** install, start, world creation, `say` and `console`, backup, config change with automatic restart, clean stop (the world saves), restore, update and remove.
- **Verified on Linux with Wurm Unlimited:** SteamCMD installs itself and the server (app 402370), the setup steps run, and the server starts headless and listens on its game and query ports. **Known issue:** on shutdown (interrupt or the console `shutdown` command) the bundled Java crashes inside `steamclient.so`. Whether the world saves fully on stop still needs checking; the original repo may hold a clue.
- **Release builds** cross-compile for Linux, Windows and macOS (amd64 and arm64) with `make release`.
- **Decisions made along the way:**
  - Linux first for milestone 2, since that's the dev machine.
  - The main registry is published at `jonsjava.github.io/anyserve-registry` (GitHub Pages for the `anyserve-registry` repo). The signed snapshot in the binary is the fallback when it can't be reached.
  - Merging definition changes to `main` publishes them: the Publish registry workflow signs the index with the key in the `registry` environment, pushes it to `anyserve-registry` with a deploy key, and commits the snapshot back. `make publish-registry` does the same by hand.
  - Vanilla only for Rust and Terraria for now.
  - Targets not built yet are listed by `anyserve targets` and refuse to install.
- **Not verified yet:** the wizard in a real terminal, Rust (10 GB download), and anything on Windows or macOS.

### Session 2 (2026-11-22): stress test with 10 random servers
- **Picked at random:** 5 Pelican/Pterodactyl eggs (Plains of Pain, Don't Starve Together, No One Survived, CryoFall, Counter-Strike 2) and 5 LinuxGSM servers (Core Keeper, Killing Floor, Stationeers, Quake 4, American Truck Simulator). All 10 are now definitions in `registry/services/`.
- **Engine features added to make them fit** (each one general, none game-specific):
  - `set` mode for config files (ini, lines, json): change only some keys in a file the game owns. Files the game creates on first start get the settings applied before every start.
  - Pinned extra downloads (`install.downloads`, sha256 required).
  - Host launchers from a fixed list (`run.via`: xvfb, dotnet, java, mono), with install hints when missing.
  - Each server gets its own `HOME` (the server folder), so `~/.steam/sdk64`, `~/.local/share/...` and Unity saves stay per instance.
  - Steps `steam_sdk` and `steamcmd_file` (copy SteamCMD's own Steam libraries).
  - SteamCMD beta branches and `login: required`, with `anyserve steam login <user>`. SteamCMD caches the session; Anyserve stores only the user name.
  - `.tar.xz` archives.
- **Gaps found that the model can't express yet:**
  - Several processes per service (DST's Caves shard). DST ships as surface-only.
  - XML config (CryoFall's `SettingsServer.xml`).
  - Two install sources (Quake 4 needs the archive plus the base game data from Steam app 2210).
  - Windows-only servers on Linux (No One Survived runs under Wine in the egg; Anyserve marks it Windows-only).
- **Test installs on Linux (the dev machine, Ubuntu 24.04):**
  - **Plains of Pain:** installs, starts, says "Server Ready", listens on both ports, stops cleanly. Saves land in the per-server `.config/unity3d`.
  - **Stationeers:** installs and runs once `LocalIpAddress 0.0.0.0` is passed (without it the game finds "no IPv4 adapters" on a host with Docker bridges, even outside Anyserve). It creates its save and stops cleanly.
  - **American Truck Simulator:** installs, the pinned sample files download, the `lines` patch sets the lobby name and ports, and the server loads Steam and listens on the query port. A full online session wasn't confirmed.
  - **Quake 4:** installs from the pinned `.tar.xz`, but can't start without the retail base paks (`pak001` to `pak012`). LinuxGSM installs from the same archive alone, so its `q4server` probably has the same problem. Worth an upstream issue once confirmed. The server also crashes when its folder path is very long.
  - **Not run here:** Core Keeper (no xvfb on the dev machine), CryoFall (no .NET), Killing Floor (needs an account that owns it), DST (needs a Klei token), CS2 (35 GB), No One Survived (Windows).
- **Effort:** the engine work was a one-time cost, about 600 lines of Go including tests. Each definition took 10 to 20 minutes to translate from the egg or LinuxGSM config. The expensive part is checking that each game actually runs: 2 of the 4 that were tested needed a fix or a note that research alone wouldn't have found.
- **What to revisit:**
  1. A definition importer for Pelican/Pterodactyl eggs. Egg `config.files` parsers map almost one-to-one to `set` formats, startup lines to `run.args` and variables to settings. It would generate a draft for a person to review, not publish automatically.
  2. A `verified` record per definition and platform, filled in by a CI test install, so the registry shows what has really been run.
  3. Several processes per service (DST caves), more `set` formats (xml, properties, yaml) and a second install source for bring-your-own game data.
  4. Host dependencies (xvfb, .NET, 32-bit libraries) become the container target's job; on bare metal they stay install-time warnings.

### Session 2b (2026-11-22): two non-game services
- **Harmony-Chat** (XRFoundation): not packageable. It isn't a server but an XREngine "project" (a React route plugin, last changed April 2022). Running it means building XREngine from source at a 2022 commit. That's a pnpm monorepo with native modules, needing MariaDB, Redis, S3-style storage and four or five Node processes (API, client, task server, instance servers through Agones). XREngine became Ethereal Engine (archived August 2024) and then iR Engine, whose packages are renamed, so Harmony would also need porting. Verdict: out of scope, and not worth reviving.
- **Lemmy:** feasible, but only as a stack:
  - **Backend:** only the 1.0 betas ship a binary (`lemmy_server.gz`, glibc 2.34+, needs `libpq`). Stable 0.19.x is Docker-only or `cargo build`.
  - **lemmy-ui:** a Node app with no release artifact, so it has to be built from source or taken from its Docker image.
  - **pict-rs** (images): static binaries, the easy part.
  - **Backing services:** PostgreSQL, plus a reverse proxy that sends `/api`, `/pictrs` and ActivityPub requests to the backend and everything else to the UI.
  - **Federation:** needs a public domain with TLS.
- **What this shows:** the definition model fits "one program that owns its files", which is games and single-binary services. Web apps are several programs plus a database and a proxy. Rebuilding Docker Compose on bare metal would cost a lot and add little.
- **Proposal:**
  - Add a `stack` kind of definition that only the container and Kubernetes targets accept: several images, named volumes, one public entry point.
  - Bare-metal definitions stay single-process, with an optional `processes:` list for game shards like DST's caves.
  - Lemmy becomes the first stack, once the Docker target exists.

### Session 2c (2026-11-22): Docker target, stacks and consoles
- **The `docker` target** runs `kind: stack` definitions through Docker Compose. Anyserve writes the compose file itself, and the security choices are fixed in Anyserve, not left to definitions:
  - Images must be pinned by digest.
  - Containers run as the invoking user, with all capabilities dropped and `no-new-privileges` set.
  - Mounts are limited to folders and files inside the instance.
  - Ports that aren't public bind to 127.0.0.1.
  - Data files stay owned by the user, so backup and removal never need root.
- **Lemmy 0.19.21** is the first stack: postgres, pictrs, lemmy, lemmy-ui and an nginx proxy, following lemmy-ansible. Verified:
  - The install takes 25 seconds once images are cached.
  - The API, the UI and ActivityPub routing all answer.
  - The admin account is created on first start; login, image upload and community creation work.
  - Update, logs and remove work.
  - Backup with `backup.stop_first` works, and so does restore.
- **Restore fix found by the Lemmy test:** for `stop_first` definitions, restore replaces the backed-up folders instead of merging into them, because stray newer database files can corrupt PostgreSQL. It refuses to restore if the safety backup fails.
- **Consoles:**
  - `anyserve console <name>` runs one command and prints the reply, or opens a live session.
  - It uses RCON when the definition declares `console.rcon`: Source RCON (CS2 and many others) or Rust's WebRCON. Both are implemented in `internal/rcon` with no new dependencies.
  - Servers without RCON fall back to the supervised stdin console, showing the server's output.
  - Stacks declare `console.exec`; Lemmy opens `psql`.
  - `console --info` gives connection details for other tools, with a reminder to tunnel the private RCON port.
  - `anyserve rcon <host:port>` reaches servers on other machines. The password comes from an environment variable or a prompt, never a flag.
- **Not done yet:** native servers in containers (the `container:` hints), Kubernetes, and Quake-style UDP rcon (Quake 4 keeps the stdin console).

### Session 3 (2026-11-22): 33 more games and apps
- **Goal:** be useful to more people. The registry went from 14 to 47 definitions: 12 more games and 21 self-hosted apps.
- **Engine features added to make them fit** (each general, none tied to one service):
  - `folder` settings, so apps can share a folder of the user's files (music, media, documents). The user picks the folder; Anyserve resolves symlinks and refuses `/`, system folders, the whole home folder and folders holding keys (`~/.ssh`, `~/.config`, `~/.gnupg` and others). Stack volumes mount them with `setting:`.
  - `--expose` at install and `config --expose/--unexpose` later, to open a stack's private web port to the network. Without it, app pages stay on 127.0.0.1.
  - `must_accept` bool settings for licences such as Mojang's EULA. Anyserve won't install until the user adds `--set key=true` themselves.
  - Installs made only of pinned `downloads` (single-jar servers), `archive.format` for URLs without an extension, and `archive.keep` for config files an update mustn't replace (Bedrock).
  - `run.via_args` (JVM flags between `java` and the jar).
  - An `exec` step that runs once, for first-start setup such as Factorio's `--create`.
  - `stop.rcon`: send the stop command over RCON, for servers with no stdin console (Palworld). `say` and the save before a backup also use RCON when the server has it.
  - RCON client fixes for Palworld: it closes the connection after each reply, and it answers with id 0 instead of the request's id.
  - Template helpers `hasPrefix` and `xml`.
  - Fixes: `config` changes to a stack now rewrite its compose file, and `logs` only shows the current instance's journal after a reinstall under the same name.
- **Games, tested on Linux:**
  - **Full run** (start, console or RCON, save, clean stop, backup where relevant): Factorio, Garry's Mod, Mindustry, Necesse, Palworld, Unturned.
  - **Started and stopped cleanly:** Arma Reforger, Project Zomboid (saves on stop), Satisfactory, Valheim.
  - **Not booted here:** Minecraft (Java and Paper) need Java 25, which the dev machine doesn't have, and the EULA wasn't accepted on the user's behalf. The vanilla install and its launch line were checked. Minecraft Bedrock wasn't run.
  - **Fixes the tests found:** Garry's Mod needs the x86-64 beta (the 32-bit build hangs) and `+ip 0.0.0.0` (otherwise srcds binds RCON to 127.0.1.1). Unturned only reads console commands from a real terminal, so it has no console in Anyserve and stops by interrupt, which saves the world.
- **Apps, tested with Docker:** every one installed, started and answered HTTP: Actual Budget, Audiobookshelf, Forgejo, Gitea, Gotify, Grafana, Immich, Jellyfin, Mealie, Memos, Miniflux, Mumble, n8n, Navidrome, ntfy, Paperless-ngx, Syncthing, Umami, Uptime Kuma and Vaultwarden.
  - Logins checked with the generated or default credentials: Gotify, Miniflux, Grafana, Vaultwarden's admin page, Umami, Mealie and Paperless-ngx. Gitea's first registered user becomes admin.
  - Paperless-ngx imported a file dropped in its consume folder. The test found that without a chosen folder the image's own one belongs to root and Paperless refuses to start, so it now watches a folder inside the instance.
- **Dropped or skipped:**
  - **linkding:** its image must start as root (uwsgi and supervisord drop privileges), which stacks don't allow.
  - **7 Days to Die:** its console is telnet only.
  - **ARK:** stopping needs several console commands in a row.
  - **TeamSpeak:** needs a licence accepted through an environment variable; worth adding with `must_accept`.

### Next
1. Windows service and launchd targets (milestone 4)
2. Native servers in containers, using each definition's `container` hints (rest of milestone 5)
3. Release binaries from a tag-triggered workflow
4. A Pelican egg importer that drafts definitions for review
5. Several processes per server (DST's caves), more `set` formats (xml, properties, yaml), and a second install source for bring-your-own game data
6. Scheduled backups and update checks
7. LinuxGSM pull request for Wurm Unlimited, and an issue about Quake 4's missing base paks
8. A telnet console (7 Days to Die), multi-command stops (ARK), and TeamSpeak

## Decided since the start

- Linux comes first for milestone 2.
- The code lives at `github.com/jonsjava/anyserve` (MIT licensed). The main registry is `github.com/jonsjava/anyserve-registry`, served by GitHub Pages.
- The registry is signed in GitHub Actions after the maintainer merges a pull request, so approving a definition is the only manual step. The signing key is a secret in the `registry` environment, limited to `main`; pull request runs never get it. (This replaced signing on the maintainer's machine.)
- Rust and Terraria are vanilla for now.

## Still open

- Securing the name: domains, and package names on winget, Homebrew and Scoop
- A direct UK IPO search, if wanted
