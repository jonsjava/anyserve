# Contributing to Anyserve

Thanks for helping. Anyserve is a hobby project, so reviews may take a while,
but contributions are welcome.

## Adding or fixing a service definition

This is the most useful contribution.

1. Read [docs/definitions.md](docs/definitions.md) and copy the closest
   existing file in `registry/services/`.
2. Pin everything: `sha256` for archives and extra downloads, digests for
   container images. To get an image's digest without pulling it:

   ```bash
   docker buildx imagetools inspect <image>:<tag>
   ```

3. Validate:

   ```bash
   make check
   make test
   ```

4. **Test it for real.** Most games have at least one quirk that only shows
   up when you run them. Keep test installs out of your real setup:

   ```bash
   export ANYSERVE_HOME=$(mktemp -d)
   go run ./cmd/anyserve install <id> --name test --start -y
   go run ./cmd/anyserve logs test -f
   go run ./cmd/anyserve console test       # try a command
   go run ./cmd/anyserve stop test          # did the world save?
   go run ./cmd/anyserve backup test
   go run ./cmd/anyserve remove test -y
   ```

5. In the pull request, say what you tested, on which OS, and what you
   couldn't test. Put anything surprising in the definition's `notes`.

When you open the pull request, CI checks formatting, validates every
definition, runs the tests with the race detector and builds for each
platform. Its summary lists the download URLs, checksums and image digests
your change adds, for review. Once the maintainer approves and merges it, the
new definitions are signed and published to the main registry automatically,
and `anyserve refresh` picks them up within a few minutes.

If a game needs something definitions can't express, don't work around it
with a script. Open an issue or add a general feature in Go: a step type,
a `set` file format, a launcher. Definitions stay declarative so the
registry can't be used to run arbitrary code on people's machines.

If you translate a definition from another project, check its license and
credit it in the pull request. The Pelican eggs and LinuxGSM are MIT.

## Porting to LinuxGSM

The project aims to port every service to
[LinuxGSM](https://github.com/GameServerManagers/LinuxGSM) too. Each
definition's `linuxgsm` field records its status. Fixes found while writing a
definition are often worth sending upstream.

## Code

- Go 1.27 or newer. Run `gofmt`, `make test` and, before a pull request,
  `make test-full` (race detector).
- Layout:

  | Package | What it does |
  |---|---|
  | `cmd/anyserve` | Entry point |
  | `internal/cli` | Commands and the guided wizard |
  | `internal/app` | Install, update, run, console and backup logic |
  | `internal/spec` | The definition schema, validation and templates |
  | `internal/registry` | Signed registry index: build, sign, verify, fetch |
  | `internal/fetch` | HTTPS downloads, archives and SteamCMD |
  | `internal/steps` | Built-in steps and config file writing/patching |
  | `internal/supervise` | Runs a server, its console socket and clean stops |
  | `internal/target` | systemd, Docker and planned targets |
  | `internal/rcon` | Source RCON and WebRCON clients |
  | `internal/instance`, `internal/paths` | Instance storage and folders |
  | `tools/regbuild` | Builds and signs the registry index |

- Keep the security rules intact: no shell from definitions, paths stay
  inside the instance, downloads are HTTPS and pinned, containers stay
  locked down. Changes to these need a clear reason.
- Don't edit the registry snapshot in `internal/registry/snapshot` or the
  published [anyserve-registry](https://github.com/jonsjava/anyserve-registry)
  by hand. The Publish registry workflow signs both when definition changes
  reach `main`.

## Reporting bugs

Open an issue with the command you ran, its output, your OS, and the
definition and version (`anyserve status <name>` shows it). Leave out
passwords and tokens: `anyserve config` hides them unless you pass
`--show-secrets`.
