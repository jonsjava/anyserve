# Service definition reference

A service definition is one YAML file in `registry/services/`, named after
its `id` (for example `terraria.yaml`). It describes where a service's
software comes from, the settings and ports a user can choose, the config
files to write and how to run, stop and talk to it.

Definitions are **data, not code**:

- There are no shell hooks. Anything a definition needs to do on the host is
  one of a fixed set of built-in step types, written in Go.
- Every path is relative and must stay inside the instance's folder.
- Every download is HTTPS. Archives and extra files should be pinned by
  SHA-256, and container images must be pinned by digest.
- Unknown fields are rejected, so a typo fails loudly.

Check your definitions with:

```bash
make check
```

There are two kinds of definition:

- **Servers** (the default) are one program that Anyserve downloads, sets up
  and supervises itself. Most game servers are servers.
- **Stacks** (`kind: stack`) are groups of containers, such as a web app with
  its database and proxy. Only the `docker` target runs them. See
  [Stacks](#stacks).

## Top-level fields

| Field | Required | Meaning |
|---|---|---|
| `schema` | yes | Always `1`. |
| `id` | yes | Lowercase letters, digits and dashes. Must match the file name. |
| `name` | yes | Display name. |
| `description` | yes | One sentence. |
| `homepage` | no | The game's or project's site. |
| `version` | yes | The definition's own version. Bump it whenever installed instances should pick up a change; `anyserve update` applies it. |
| `kind` | no | `server` (default) or `stack`. |
| `platforms` | servers | Operating systems with a native build: `linux`, `windows`, `darwin`. Stacks leave this out. |
| `install` | servers | Where the software comes from. |
| `steps` | no | Built-in setup steps, run after every install and update. |
| `settings` | no | Values the user chooses. |
| `ports` | yes | Network ports. |
| `files` | no | Config files Anyserve writes or patches. |
| `run` | servers | How to start the server, per OS. |
| `stop` | no | How to stop it cleanly. |
| `console` | no | Chat broadcast, save command, RCON or a stack console. |
| `backup` | no | What to back up. |
| `stack` | stacks | The containers. |
| `container` | no | Hints for running a server in a container later (not used yet). |
| `linuxgsm` | no | How the service relates to LinuxGSM. |
| `notes` | no | Things users should know, shown by `info` and the wizard. |

## Templates

Many string fields are Go [`text/template`](https://pkg.go.dev/text/template)
templates. They can use:

| Value | Meaning |
|---|---|
| `.Settings.<key>` | A setting's value, typed (`int`, `bool` or string). |
| `.Ports.<name>` | A port number. |
| `.Dir` | The absolute server folder on the host. |
| `.Name` | The instance name. |
| `.OS` | The host OS (`linux`, `windows`, `darwin`). |

Helpers: `yesno`, `truefalse` and `onezero` turn a bool into `yes`/`no`,
`true`/`false` or `1`/`0`. `lower` and `upper` change case,
and `hasPrefix` tests the start of a value (`{{if hasPrefix .Settings.url "https://"}}`). `json` renders a
value as a JSON literal, which gives a quoted, escaped string for JSON, HJSON
and YAML config files.

In `run.args`, an argument that renders to an empty string is dropped, so an
optional flag and its value can be written as:

```yaml
- "{{if .Settings.password}}-password{{end}}"
- "{{.Settings.password}}"
```

## install

Set one source, `steamcmd` or `archive`, and optionally `downloads`. A
server that is a single file, such as a Java `.jar`, can use `downloads`
alone.

### steamcmd

```yaml
install:
  steamcmd:
    app_id: 258550
    beta: experimental        # optional branch
    login: required           # optional; default anonymous
    platform:                 # optional; force another OS's build
      linux: windows
```

Anyserve installs its own private SteamCMD. With `login: required`, the user
signs in once with `anyserve steam login <user>`. SteamCMD keeps the session,
and Anyserve stores only the user name, never the password.

### archive

```yaml
install:
  archive:
    url: https://example.com/server-1.2.zip
    sha256: <64 lowercase hex characters>
    subdir:                   # optional folder inside the archive, per OS
      linux: 1.2/Linux
      "*": 1.2/Common         # any OS without its own entry
    format: tar.xz            # only if the URL doesn't end in the type
```

Supported archives: `.zip`, `.tar.gz`, `.tgz`, `.tar.xz` and `.txz`. When a
download URL has no extension (Factorio's, for example), set `format` to
`zip`, `tar.gz` or `tar.xz`. Symlinks
and paths that would leave the install folder are skipped or rejected. Files
already in the server folder that aren't in the archive are left alone, so
saves survive updates.

### downloads

Extra single files, fetched after the main source. Each must be pinned.

```yaml
install:
  downloads:
    - url: https://example.com/default-config.sii
      sha256: <64 hex>
      to: .local/share/Game/server_config.sii
      always: false           # true replaces the file on every update
```

By default an existing file is kept, because users often replace these files
with their own.

## steps

Steps run in order after every install and update. Each has a `type` and
may be limited to some systems with `os: [linux]`.

| Type | Fields | What it does |
|---|---|---|
| `copy_dir_if_missing` | `from`, `to` | Copies a folder unless `to` exists. |
| `copy_file_if_missing` | `from`, `to` | Copies a file unless `to` exists. |
| `copy_file` | `from`, `to` | Copies a file, replacing it. |
| `touch` | `path` | Creates an empty file unless it exists. |
| `mkdir` | `path` | Creates a folder. |
| `chmod_exec` | `path` | Marks a file executable (does nothing on Windows). |
| `steam_sdk` | | Copies SteamCMD's `steamclient.so` into `.steam/sdk32` and `.steam/sdk64`, where many Steam servers look for it. SteamCMD installs only. |
| `steamcmd_file` | `from`, `to` | Copies a file from Anyserve's SteamCMD folder (for example `linux32/steamclient.so`). SteamCMD installs only. |
| `exec` | `command`, `args`, `creates` | Runs the server's own `command` (a file in the server folder) with `args` once, unless `creates` exists. Runs after config files are written. Use it for one-time setup the server does itself, like creating a first world. `args` are templates. |

```yaml
steps:
  - type: exec
    command: bin/x64/factorio
    args: [--create, saves/world.zip]
    creates: saves/world.zip
```

If a service needs something these can't do, add a new step type in
`internal/steps` instead of reaching for a script.

## settings

```yaml
settings:
  - key: max_players          # lowercase, digits and underscores
    prompt: Maximum players   # shown by the wizard
    description: Optional longer help.
    type: int                 # string, int, bool, choice or folder
    default: 8
    min: 1                    # int only
    max: 255
  - key: difficulty
    prompt: Difficulty
    type: choice
    default: "1"
    choices:
      - { value: "0", label: Easy }
      - { value: "1", label: Normal }
  - key: rcon_password
    prompt: RCON password (leave empty to generate one)
    type: string
    secret: true              # hidden in prompts and output
    generate: true            # empty means a random password
  - key: motd
    prompt: Message of the day
    type: string
    optional: true            # empty is allowed
```

A `folder` setting asks for a folder on the user's machine, such as a music
library, and only stacks use it (see [Stacks](#stacks)). The value must be an
existing folder, given as an absolute path or starting with `~/`. Anyserve
refuses system folders (`/etc`, `/usr` and so on), the whole home folder, and
folders that hold keys or other programs' settings (`~/.ssh`, `~/.config`,
`~/.local`), so a definition can't talk someone into sharing them.

```yaml
  - key: music_folder
    prompt: Your music folder (e.g. ~/Music)
    type: folder
    default: ~/Music
```

A `string` setting with no default that isn't `optional` or `generate` is
required. That suits tokens the user must bring, such as a game server login
token. Values can't contain line breaks.

Generated values are saved with the instance, so they stay the same across
updates. Users see them with `anyserve config <name> --show-secrets`.

## ports

```yaml
ports:
  - name: game
    port: 7777
    protocol: udp             # tcp, udp or both
    public: true              # players need this forwarded
    description: Players connect here.
```

Users can change any port with `--port name=number`. Admin ports such as
RCON should be `public: false`. For stacks, non-public ports bind to
127.0.0.1 only, until the user opens one with `--expose` (for a web UI on a
home network, say).

## files

A file is written whole from a template, patched with `set`, or both.

### Template files

```yaml
files:
  - path: serverconfig.txt
    overwrite: true           # rewrite on reconfigure and update
    template: |
      maxplayers={{.Settings.max_players}}
      port={{.Ports.game}}
```

Without `overwrite`, the file is written only if it's missing, so hand edits
survive. Files are written with mode 0600, because they often hold
passwords.

### Patching files with set

Many games create their own config file and expect people to edit it. `set`
changes only the listed keys and leaves everything else alone.

```yaml
files:
  - path: WRSH/Saved/Config/WindowsServer/Game.ini
    format: ini
    set:
      ServerSetting.MaxPlayers: "{{.Settings.max_players}}"
      /Script/Engine.GameSession.MaxPlayers: "{{.Settings.max_players}}"
```

| Format | Key | Behavior |
|---|---|---|
| `ini` | `Section.key` | The section is everything before the last dot, so Unreal sections like `/Script/Engine.GameSession` work. Missing keys are added to their section; missing sections are created. |
| `lines` | a line prefix | Every line starting with the prefix (ignoring indentation) is replaced by the value, keeping its indentation. Unmatched keys are appended. |
| `json` | dotted path | Sets a value inside nested objects. A value keeps the type of the value it replaces; new values become numbers or booleans when they look like one. |

If the file doesn't exist yet and there's no `template`, Anyserve applies the
settings on the next start, because servers' `set` files are patched before
every start (stacks' files are written at install, update and reconfigure). That handles games that write their config on first run. With a
`template` as well, the template is the starting content when the file is
missing.

Line endings (LF or CRLF) are kept.

## run

```yaml
run:
  linux:
    command: RustDedicated            # a file in the server folder
    via: [xvfb]                       # optional host launchers
    via_args: []                      # optional arguments for the launcher
    workdir: bin64                    # optional; default the server folder
    args: [-batchmode, "+server.port", "{{.Ports.game}}"]
    env:
      LD_LIBRARY_PATH: "{{.Dir}}/RustDedicated_Data/Plugins"
  windows:
    command: RustDedicated.exe
    args: [-batchmode]
```

Every OS in `platforms` needs an entry. `command` is always a file inside the
server folder; definitions can't run arbitrary host programs.

`via` starts the command through a host program from a fixed list. The user
is warned at install if it's missing. `via_args` go to the launcher, before
`command`: JVM options and `-jar` for Java servers, for example.

```yaml
run:
  linux:
    command: server.jar
    via: [java]
    via_args: ["-Xmx{{.Settings.memory_mb}}M", "-jar"]
    args: [nogui]
```

| Launcher | Runs | Typical use |
|---|---|---|
| `xvfb` | `xvfb-run -a` | Servers that need a display, like Core Keeper |
| `dotnet` | `dotnet` | .NET servers, where `command` is the `.dll` |
| `java` | `java` | Java servers |
| `mono` | `mono` | Mono servers |

On Linux and macOS each server runs with `HOME` set to its own server
folder. Files games keep in the home folder, such as `~/.steam/sdk64`,
`~/.local/share/...` and Unity saves under `~/.config/unity3d`, stay inside
the instance, so backups find them and two instances don't collide.

## stop

```yaml
stop:
  command: exit               # typed into the server console
  timeout_seconds: 60         # then the server is killed
```

Without `command`, Anyserve sends an interrupt (Ctrl-C), then kills the
server after the timeout.

For servers that don't read their console, set `rcon: true` to send the stop
command over the definition's RCON instead:

```yaml
stop:
  command: Shutdown 1
  rcon: true
```

## console

```yaml
console:
  say: "say {{.Message}}"     # broadcast; used for restart warnings
  save: save                  # run before backups
  rcon:                       # optional remote console
    protocol: webrcon         # source or webrcon
    port: rcon                # a port name from ports (TCP)
    password: "{{.Settings.rcon_password}}"
```

`anyserve console <name>` uses RCON when it's declared, so commands get real
replies. Chat broadcasts (`say`) and the save before a backup go over RCON
too, because many servers ignore their standard input. and `anyserve console <name> --info` shows the details for other
RCON tools. Without RCON, Anyserve types into the server's own console and
shows what the server prints.

| Protocol | Used by |
|---|---|
| `source` | Valve's Source RCON over TCP: Counter-Strike, Minecraft, ARK, Conan Exiles and many others |
| `webrcon` | Facepunch's WebRCON (JSON over WebSocket): Rust |

Remember to pass the password to the server itself too, usually in
`run.args` or a config file.

Stacks use `exec` instead (see below).

## backup

```yaml
backup:
  paths: [worlds, serverconfig.txt]
  stop_first: false
```

Paths are relative to the server folder. Backups are `.tar.gz` files, and the
newest 10 are kept. If the server is running and has `console.save`, it's
asked to save first.

Set `stop_first: true` for anything with a database. Anyserve then stops the
service for the backup and starts it again afterwards. Restoring replaces
the backed-up folders outright instead of merging into them, because stray
newer files can corrupt a database.

## Stacks

```yaml
schema: 1
id: lemmy
kind: stack
# no platforms, install, steps or run

ports:
  - { name: http, port: 10633, protocol: tcp, public: false }

files:
  - path: config/app.conf
    overwrite: true
    template: "..."

stack:
  services:
    - name: postgres
      image: postgres:16.16-alpine@sha256:<64 hex>
      env:
        POSTGRES_PASSWORD: "{{.Settings.postgres_password}}"
      volumes:
        - { path: data/postgres, target: /var/lib/postgresql/data }
      health:
        command: [pg_isready, -U, lemmy]
        interval_seconds: 5
        retries: 20
      shm_mb: 256
    - name: app
      image: example/app:1.0@sha256:<64 hex>
      command: [serve, --port, "8080"]
      volumes:
        - { path: config/app.conf, target: /etc/app.conf, read_only: true }
      ports:
        - { port: http, target: 8080 }
      depends_on: [postgres]

console:
  exec:
    service: postgres
    command: [psql, -U, lemmy]
    description: the PostgreSQL shell

backup:
  paths: [data, config]
  stop_first: true
```

| Field | Meaning |
|---|---|
| `name` | The container's name, and its host name on the stack's private network. |
| `image` | Must be pinned by digest: `name:tag@sha256:...`. |
| `command` | Replaces the image's command. Templates allowed. |
| `env` | Environment variables. Templates allowed. |
| `volumes` | Mounts from the server folder (`path`), or a folder the user chose in a `folder` setting (`setting`). A path listed in `files` is mounted as a file; any other path is a folder, created before the stack starts. An optional folder setting left empty skips its mount. Backups only cover the server folder, not chosen folders. |
| `ports` | Publishes a port from `ports` (by name) from the container's `target` port. Each port is published once. |
| `depends_on` | Starts these services first, waiting until they're healthy if they have a health check. |
| `health` | A command run inside the container; exit status 0 means healthy. |
| `shm_mb` | Shared memory size, which databases often need raised. |

Anyserve writes the Compose file itself, and some things are fixed so that
definitions can't change them:

- every container runs as the user who installed the instance, so its files
  stay readable, backed up and removable without root;
- all Linux capabilities are dropped and `no-new-privileges` is set;
- there's no way to ask for privileged mode, the host network, host paths or
  the Docker socket;
- containers restart unless stopped, and logs are capped at 30 MB each;
- ports that aren't `public` listen on 127.0.0.1 only, unless the user opens
  them with `anyserve install --expose <port>` or
  `anyserve config <name> --expose <port>`.

A folder from a `folder` setting is mounted like this:

```yaml
      volumes:
        - { path: data, target: /data }
        - { setting: music_folder, target: /music, read_only: true }
```

Images that insist on running as root won't work. Prefer images that run as
any user, such as `nginxinc/nginx-unprivileged` instead of `nginx`.

`console.exec` gives a stack a console. `anyserve console <name>` opens it
interactively, and `anyserve console <name> '<input>'` passes one input on
stdin (for example, one SQL statement to `psql`).

## linuxgsm

```yaml
linuxgsm:
  status: upstream            # upstream, contributed or missing
  name: rustserver            # LinuxGSM's server name, if any
  notes: Anything useful for porting.
```

The project aims to port every service to LinuxGSM. This records where each
one stands.

## Checklist for a new definition

1. Copy the closest existing definition.
2. Pin every download (sha256) and image (digest).
3. Run `make check`.
4. Install it for real (use `ANYSERVE_HOME` to keep test installs separate):
   start it, connect, stop it and check that the world saved, back it up,
   and restore the backup.
5. Note anything surprising in `notes`. Most games have at least one quirk.
