// Package spec defines the Anyserve service definition format: a declarative
// description of how to download, configure and run one server or service.
//
// Definitions are data, not code. They can name files to download, files to
// write from templates, a fixed set of built-in step types and the command to
// run, but they can never run arbitrary shell. Every path they mention must
// stay inside the instance directory.
package spec

import (
	"fmt"
	"math"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only definition schema this build understands.
const SchemaVersion = 1

// Known operating systems, using Go's GOOS names.
var KnownOS = []string{"linux", "windows", "darwin"}

// Definition is one service definition file.
type Definition struct {
	Schema      int    `yaml:"schema" json:"schema"`
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Homepage    string `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	// Version is the definition's own version. Bump it when the definition
	// changes in a way installed instances should pick up.
	Version string `yaml:"version" json:"version"`
	// Kind is "server" (the default) or "stack".
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Platforms lists the operating systems with a native server build.
	// Stacks leave it out: they run wherever a container target runs.
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`

	Install   Install        `yaml:"install" json:"install"`
	Steps     []Step         `yaml:"steps,omitempty" json:"steps,omitempty"`
	Settings  []Setting      `yaml:"settings,omitempty" json:"settings,omitempty"`
	Ports     []Port         `yaml:"ports" json:"ports"`
	Files     []File         `yaml:"files,omitempty" json:"files,omitempty"`
	Run       map[string]Run `yaml:"run" json:"run"`
	Stop      Stop           `yaml:"stop,omitempty" json:"stop,omitempty"`
	Console   Console        `yaml:"console,omitempty" json:"console,omitempty"`
	Backup    Backup         `yaml:"backup,omitempty" json:"backup,omitempty"`
	Container *Container     `yaml:"container,omitempty" json:"container,omitempty"`
	Stack     *Stack         `yaml:"stack,omitempty" json:"stack,omitempty"`
	LinuxGSM  *LinuxGSM      `yaml:"linuxgsm,omitempty" json:"linuxgsm,omitempty"`
	Notes     []string       `yaml:"notes,omitempty" json:"notes,omitempty"`
	Extra     map[string]any `yaml:",inline" json:"-"`
}

// Install says where the server files come from. Exactly one source is set.
type Install struct {
	SteamCMD *SteamCMD `yaml:"steamcmd,omitempty" json:"steamcmd,omitempty"`
	Archive  *Archive  `yaml:"archive,omitempty" json:"archive,omitempty"`
	// Downloads are extra single files fetched after the main source, each
	// pinned by checksum.
	Downloads []Download `yaml:"downloads,omitempty" json:"downloads,omitempty"`
}

// Download is one extra file to fetch into the server directory.
type Download struct {
	URL    string `yaml:"url" json:"url"`
	SHA256 string `yaml:"sha256" json:"sha256"`
	To     string `yaml:"to" json:"to"`
	// Always replaces the file on every update. By default an existing
	// file is kept, because users often replace these with their own.
	Always bool `yaml:"always,omitempty" json:"always,omitempty"`
}

// SteamCMD installs an app anonymously through Valve's SteamCMD.
type SteamCMD struct {
	AppID int `yaml:"app_id" json:"app_id"`
	// Platform asks SteamCMD for a specific OS's build, per host OS.
	// Usually left empty, which means "the host's own OS".
	Platform map[string]string `yaml:"platform,omitempty" json:"platform,omitempty"`
	// Beta installs a beta branch instead of the public one.
	Beta string `yaml:"beta,omitempty" json:"beta,omitempty"`
	// Login is "anonymous" (the default) or "required" for apps that only
	// download with a Steam account. Anyserve never stores the password:
	// `anyserve steam login` lets SteamCMD cache the session itself.
	Login string `yaml:"login,omitempty" json:"login,omitempty"`
}

// Archive downloads a .zip, .tar.gz or .tar.xz and extracts part of it.
type Archive struct {
	URL    string `yaml:"url" json:"url"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	// Subdir picks the folder inside the archive to install, per OS.
	// "*" applies to every OS without its own entry.
	Subdir map[string]string `yaml:"subdir,omitempty" json:"subdir,omitempty"`
}

// Step is one built-in install step. Steps run in order after the files are
// downloaded, and again after every update.
type Step struct {
	// Type is one of the StepTypes.
	Type string `yaml:"type" json:"type"`
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	To   string `yaml:"to,omitempty" json:"to,omitempty"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// OS limits the step to some operating systems. Empty means all.
	OS []string `yaml:"os,omitempty" json:"os,omitempty"`
}

// StepTypes are the built-in step types a definition may use.
var StepTypes = map[string]string{
	"copy_dir_if_missing":  "copy directory `from` to `to` unless `to` exists",
	"copy_file_if_missing": "copy file `from` to `to` unless `to` exists",
	"copy_file":            "copy file `from` to `to`, replacing it",
	"touch":                "create an empty file at `path` unless it exists",
	"mkdir":                "create directory `path`",
	"chmod_exec":           "mark `path` as executable (no-op on Windows)",
	"steamcmd_file":        "copy file `from` (relative to Anyserve's SteamCMD folder, e.g. linux32/steamclient.so) to `to`",
	"steam_sdk":            "copy SteamCMD's steamclient libraries into .steam/sdk32 and .steam/sdk64, where many Steam servers look for them",
}

// Launcher is a host program a server can be started through. The list is
// fixed in Anyserve so definitions can't run arbitrary host commands.
type Launcher struct {
	Program string
	Args    []string
	// Install tells the user how to get the program.
	Install string
}

// Launchers are the host launchers definitions may name in run.via.
var Launchers = map[string]Launcher{
	"xvfb":   {Program: "xvfb-run", Args: []string{"-a"}, Install: "a virtual display; on Debian or Ubuntu: sudo apt install xvfb"},
	"dotnet": {Program: "dotnet", Install: "the .NET runtime; see https://learn.microsoft.com/dotnet/core/install/linux"},
	"java":   {Program: "java", Install: "a Java runtime; on Debian or Ubuntu: sudo apt install default-jre-headless"},
	"mono":   {Program: "mono", Install: "the Mono runtime; on Debian or Ubuntu: sudo apt install mono-runtime"},
}

// Setting is a value the user chooses at install time.
type Setting struct {
	Key         string   `yaml:"key" json:"key"`
	Prompt      string   `yaml:"prompt" json:"prompt"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string   `yaml:"type" json:"type"` // string, int, bool, choice
	Default     any      `yaml:"default,omitempty" json:"default,omitempty"`
	Choices     []Choice `yaml:"choices,omitempty" json:"choices,omitempty"`
	// Secret hides the value in prompts and output.
	Secret bool `yaml:"secret,omitempty" json:"secret,omitempty"`
	// Generate fills an empty value with a random password.
	Generate bool `yaml:"generate,omitempty" json:"generate,omitempty"`
	// Optional allows an empty value.
	Optional bool `yaml:"optional,omitempty" json:"optional,omitempty"`
	Min      *int `yaml:"min,omitempty" json:"min,omitempty"`
	Max      *int `yaml:"max,omitempty" json:"max,omitempty"`
}

// Choice is one option of a choice setting.
type Choice struct {
	Value string `yaml:"value" json:"value"`
	Label string `yaml:"label" json:"label"`
}

// Port is a network port the server listens on.
type Port struct {
	Name        string `yaml:"name" json:"name"`
	Port        int    `yaml:"port" json:"port"`
	Protocol    string `yaml:"protocol" json:"protocol"` // tcp, udp, both
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Public says players need this port forwarded. Admin ports like RCON
	// are usually not public.
	Public bool `yaml:"public" json:"public"`
}

// File is a config file Anyserve manages. It works in one of two ways:
//
//   - template: Anyserve writes the whole file from a template.
//   - set: Anyserve changes only some values in a file, leaving the rest
//     alone. This suits files the game creates or that users hand-edit.
//     Template, if also given, is the starting content when the file is
//     missing.
type File struct {
	Path     string `yaml:"path" json:"path"`
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
	// Overwrite rewrites a templated file on reconfigure and update.
	// Without it the file is only written when missing, so hand edits
	// survive. Files with set are always updated.
	Overwrite bool `yaml:"overwrite,omitempty" json:"overwrite,omitempty"`
	// Format says how to read the file for set: ini (keys are
	// "Section.key"), lines (keys are line prefixes; the whole line is
	// replaced) or json (keys are dotted paths into objects).
	Format string            `yaml:"format,omitempty" json:"format,omitempty"`
	Set    map[string]string `yaml:"set,omitempty" json:"set,omitempty"`
	OS     []string          `yaml:"os,omitempty" json:"os,omitempty"`
}

// FileFormats are the formats `set` understands.
var FileFormats = []string{"ini", "lines", "json"}

// Run says how to start the server on one OS.
type Run struct {
	// Command is a file in the server directory: the server binary, or the
	// program a launcher in Via runs (such as a .dll for dotnet).
	Command string `yaml:"command" json:"command"`
	// Via lists host launchers to start Command through, outermost first,
	// from the built-in Launchers list (e.g. [xvfb], [dotnet]).
	Via     []string          `yaml:"via,omitempty" json:"via,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Workdir string            `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// Stop says how to shut the server down cleanly.
type Stop struct {
	// Command is written to the server's console (stdin) to stop it.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// TimeoutSeconds is how long to wait before killing the process.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
}

// Console describes the server's stdin console.
type Console struct {
	// Say is a template for broadcasting a message, e.g. "say {{.Message}}".
	Say string `yaml:"say,omitempty" json:"say,omitempty"`
	// Save is a command that saves the world, used before backups.
	Save string `yaml:"save,omitempty" json:"save,omitempty"`
	// RCON describes the server's remote console. When set, `anyserve
	// console` uses it, so commands get real replies, and other RCON tools
	// can connect with the details `anyserve console --info` shows.
	RCON *RCON `yaml:"rcon,omitempty" json:"rcon,omitempty"`
	// Exec gives a stack a console: a program run inside one container,
	// such as a database shell.
	Exec *ConsoleExec `yaml:"exec,omitempty" json:"exec,omitempty"`
}

// RCON is a server's remote console.
type RCON struct {
	// Protocol is source or webrcon.
	Protocol string `yaml:"protocol" json:"protocol"`
	// Port names the entry in ports that RCON listens on.
	Port string `yaml:"port" json:"port"`
	// Password is a template, usually {{.Settings.rcon_password}}.
	Password string `yaml:"password" json:"password"`
}

// ConsoleExec runs a program inside one of a stack's containers.
type ConsoleExec struct {
	Service string   `yaml:"service" json:"service"`
	Command []string `yaml:"command" json:"command"`
	// Description says what the console is, e.g. "PostgreSQL shell".
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Backup lists what to back up, relative to the instance directory.
type Backup struct {
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// StopFirst stops a running service for the backup and starts it again
	// afterwards. Databases need this, because copying their files while
	// they run can give a backup that won't restore.
	StopFirst bool `yaml:"stop_first,omitempty" json:"stop_first,omitempty"`
}

// Container holds hints for the container and Kubernetes targets.
type Container struct {
	// Image is the base image the server runs in. Server files are mounted
	// into it, never baked in, so nothing non-redistributable is published.
	Image string `yaml:"image" json:"image"`
	// Packages to install on top of the image (Debian package names).
	Packages []string `yaml:"packages,omitempty" json:"packages,omitempty"`
}

// LinuxGSM records how this service relates to LinuxGSM.
type LinuxGSM struct {
	// Status is one of: upstream (LinuxGSM already supports it),
	// contributed (we sent it upstream), missing (not in LinuxGSM yet).
	Status string `yaml:"status" json:"status"`
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Notes  string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Parse reads a definition from YAML. It rejects unknown fields so typos in
// definitions fail loudly instead of being ignored.
func Parse(data []byte) (*Definition, error) {
	var d Definition
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}
	if len(d.Extra) > 0 {
		keys := make([]string, 0, len(d.Extra))
		for k := range d.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("parse definition: unknown field(s): %s", strings.Join(keys, ", "))
	}
	return &d, nil
}

// LoadFile reads and validates a definition file.
func LoadFile(p string) (*Definition, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	d, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return d, nil
}

var (
	idRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// ValidationError collects every problem found in a definition.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid definition:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Validate checks the definition and returns every problem it finds.
func (d *Definition) Validate() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if d.Schema != SchemaVersion {
		add("schema must be %d (got %d)", SchemaVersion, d.Schema)
	}
	if !idRe.MatchString(d.ID) {
		add("id %q must be lowercase letters, digits and dashes", d.ID)
	}
	if strings.TrimSpace(d.Name) == "" {
		add("name is required")
	}
	if strings.TrimSpace(d.Description) == "" {
		add("description is required")
	}
	if strings.TrimSpace(d.Version) == "" {
		add("version is required")
	}
	stack := false
	switch d.Kind {
	case "", KindServer:
	case KindStack:
		stack = true
		d.validateStack(add)
	default:
		add("kind must be server or stack")
	}
	if d.Stack != nil && !stack {
		add("stack: only allowed with kind: stack")
	}
	if !stack && len(d.Platforms) == 0 {
		add("platforms must list at least one OS")
	}
	if stack && len(d.Platforms) > 0 {
		add("platforms: stacks run wherever containers do; leave platforms out")
	}
	for _, os := range d.Platforms {
		if !isKnownOS(os) {
			add("platforms: unknown OS %q (use linux, windows or darwin)", os)
		}
	}

	// Install source.
	switch {
	case stack:
	case d.Install.SteamCMD != nil && d.Install.Archive != nil:
		add("install: set only one of steamcmd or archive")
	case d.Install.SteamCMD != nil:
		if d.Install.SteamCMD.AppID <= 0 {
			add("install.steamcmd.app_id must be a positive number")
		}
		for os, plat := range d.Install.SteamCMD.Platform {
			if !isKnownOS(os) {
				add("install.steamcmd.platform: unknown host OS %q", os)
			}
			if plat != "linux" && plat != "windows" && plat != "macos" {
				add("install.steamcmd.platform[%s]: must be linux, windows or macos", os)
			}
		}
		switch d.Install.SteamCMD.Login {
		case "", "anonymous", "required":
		default:
			add("install.steamcmd.login must be anonymous or required")
		}
		if b := d.Install.SteamCMD.Beta; b != "" && !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(b) {
			add("install.steamcmd.beta must be a plain branch name")
		}
	case d.Install.Archive != nil:
		a := d.Install.Archive
		if !strings.HasPrefix(a.URL, "https://") {
			add("install.archive.url must be an https:// URL")
		}
		if a.SHA256 != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a.SHA256) {
			add("install.archive.sha256 must be 64 lowercase hex characters")
		}
		if !ArchiveURL(a.URL) {
			add("install.archive.url must end in .zip, .tar.gz, .tgz, .tar.xz or .txz")
		}
		for os, sub := range a.Subdir {
			if os != "*" && !isKnownOS(os) {
				add("install.archive.subdir: unknown OS %q", os)
			}
			if err := checkRelPath(sub); err != nil {
				add("install.archive.subdir[%s]: %v", os, err)
			}
		}
	default:
		add("install: set one of steamcmd or archive")
	}

	for i, dl := range d.Install.Downloads {
		where := fmt.Sprintf("install.downloads[%d]", i)
		if !strings.HasPrefix(dl.URL, "https://") {
			add("%s: url must be https://", where)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(dl.SHA256) {
			add("%s: sha256 is required (64 lowercase hex characters)", where)
		}
		if err := checkRelPath(dl.To); err != nil {
			add("%s: to: %v", where, err)
		}
	}

	// Steps.
	for i, s := range d.Steps {
		where := fmt.Sprintf("steps[%d] (%s)", i, s.Type)
		if _, ok := StepTypes[s.Type]; !ok {
			add("%s: unknown step type; known types: %s", where, strings.Join(sortedKeys(StepTypes), ", "))
			continue
		}
		for _, os := range s.OS {
			if !isKnownOS(os) {
				add("%s: unknown OS %q", where, os)
			}
		}
		switch s.Type {
		case "steam_sdk", "steamcmd_file":
			if s.Type == "steamcmd_file" {
				for name, v := range map[string]string{"from": s.From, "to": s.To} {
					if v == "" {
						add("%s: %s is required", where, name)
					} else if err := checkRelPath(v); err != nil {
						add("%s: %s: %v", where, name, err)
					}
				}
			}
			if d.Install.SteamCMD == nil {
				add("%s: only works for SteamCMD installs", where)
			}
		case "copy_dir_if_missing", "copy_file_if_missing", "copy_file":
			for name, v := range map[string]string{"from": s.From, "to": s.To} {
				if v == "" {
					add("%s: %s is required", where, name)
				} else if err := checkRelPath(v); err != nil {
					add("%s: %s: %v", where, name, err)
				}
			}
		default:
			if s.Path == "" {
				add("%s: path is required", where)
			} else if err := checkRelPath(s.Path); err != nil {
				add("%s: path: %v", where, err)
			}
		}
	}

	// Settings.
	seenKeys := map[string]bool{}
	for i, s := range d.Settings {
		where := fmt.Sprintf("settings[%d] (%s)", i, s.Key)
		if !keyRe.MatchString(s.Key) {
			add("%s: key must be lowercase letters, digits and underscores", where)
		}
		if seenKeys[s.Key] {
			add("%s: duplicate key", where)
		}
		seenKeys[s.Key] = true
		if strings.TrimSpace(s.Prompt) == "" {
			add("%s: prompt is required", where)
		}
		switch s.Type {
		case "string":
		case "int":
			if s.Default != nil {
				if _, ok := wholeNumber(s.Default); !ok {
					add("%s: default must be a whole number", where)
				}
			}
		case "bool":
			if s.Default != nil {
				if _, ok := s.Default.(bool); !ok {
					add("%s: default must be true or false", where)
				}
			}
		case "choice":
			if len(s.Choices) == 0 {
				add("%s: choice settings need choices", where)
			}
			if s.Default != nil {
				ok := false
				for _, c := range s.Choices {
					if s.DefaultString() == c.Value {
						ok = true
					}
				}
				if !ok {
					add("%s: default %v is not one of the choices", where, s.Default)
				}
			}
		default:
			add("%s: type must be string, int, bool or choice", where)
		}
		if s.Generate && s.Type != "string" {
			add("%s: generate only works with string settings", where)
		}
	}

	// Ports.
	seenPorts := map[string]bool{}
	for i, pt := range d.Ports {
		where := fmt.Sprintf("ports[%d] (%s)", i, pt.Name)
		if !keyRe.MatchString(pt.Name) {
			add("%s: name must be lowercase letters, digits and underscores", where)
		}
		if seenPorts[pt.Name] {
			add("%s: duplicate name", where)
		}
		seenPorts[pt.Name] = true
		if pt.Port < 1 || pt.Port > 65535 {
			add("%s: port must be 1-65535", where)
		}
		if pt.Protocol != "tcp" && pt.Protocol != "udp" && pt.Protocol != "both" {
			add("%s: protocol must be tcp, udp or both", where)
		}
	}
	if len(d.Ports) == 0 {
		add("ports: list at least one port")
	}

	// Files.
	for i, f := range d.Files {
		where := fmt.Sprintf("files[%d] (%s)", i, f.Path)
		if err := checkRelPath(f.Path); err != nil {
			add("%s: path: %v", where, err)
		}
		if f.Template != "" {
			if _, err := template.New("f").Funcs(funcs).Parse(f.Template); err != nil {
				add("%s: template: %v", where, err)
			}
		}
		if len(f.Set) == 0 && f.Template == "" {
			add("%s: needs a template, set, or both", where)
		}
		if len(f.Set) > 0 {
			ok := false
			for _, ff := range FileFormats {
				if f.Format == ff {
					ok = true
				}
			}
			if !ok {
				add("%s: set needs format: %s", where, strings.Join(FileFormats, ", "))
			}
			for k, v := range f.Set {
				if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "\r\n") {
					add("%s: set key %q must be a single non-empty line", where, k)
				}
				if f.Format == "ini" && !strings.Contains(k, ".") {
					add("%s: ini key %q must be Section.key", where, k)
				}
				if _, err := template.New("v").Funcs(funcs).Parse(v); err != nil {
					add("%s: set[%s]: template: %v", where, k, err)
				}
			}
		}
	}

	// Run.
	for _, os := range d.Platforms {
		if _, ok := d.Run[os]; !ok {
			add("run: no entry for platform %q", os)
		}
	}
	for os, r := range d.Run {
		where := "run." + os
		if !isKnownOS(os) {
			add("%s: unknown OS", where)
		}
		if r.Command == "" {
			add("%s: command is required", where)
		} else if err := checkRelPath(r.Command); err != nil {
			add("%s: command: %v (commands run from the instance directory)", where, err)
		}
		for _, v := range r.Via {
			if _, ok := Launchers[v]; !ok {
				add("%s.via: unknown launcher %q (known: %s)", where, v, strings.Join(sortedKeys(Launchers), ", "))
			}
		}
		if r.Workdir != "" {
			if err := checkRelPath(r.Workdir); err != nil {
				add("%s: workdir: %v", where, err)
			}
		}
		for j, a := range r.Args {
			if _, err := template.New("a").Funcs(funcs).Parse(a); err != nil {
				add("%s.args[%d]: template: %v", where, j, err)
			}
		}
		for k, v := range r.Env {
			if _, err := template.New("e").Funcs(funcs).Parse(v); err != nil {
				add("%s.env[%s]: template: %v", where, k, err)
			}
		}
	}

	if d.Stop.TimeoutSeconds < 0 {
		add("stop.timeout_seconds must not be negative")
	}
	if r := d.Console.RCON; r != nil {
		if r.Protocol != "source" && r.Protocol != "webrcon" {
			add("console.rcon.protocol must be source or webrcon")
		}
		if pt, ok := d.Port(r.Port); !ok {
			add("console.rcon.port: no port named %q", r.Port)
		} else if pt.Protocol == "udp" {
			add("console.rcon.port: %s RCON runs over TCP, but port %q is UDP only", r.Protocol, r.Port)
		}
		if r.Password == "" {
			add("console.rcon.password is required")
		} else if _, err := template.New("p").Funcs(funcs).Parse(r.Password); err != nil {
			add("console.rcon.password: template: %v", err)
		}
		if stack {
			add("console.rcon: not supported for stacks yet; use console.exec")
		}
	}
	if x := d.Console.Exec; x != nil {
		if !stack {
			add("console.exec: only for stacks (servers use their own console)")
		} else {
			found := false
			for _, sv := range d.Stack.Services {
				found = found || sv.Name == x.Service
			}
			if !found {
				add("console.exec.service: %q isn't a service in this stack", x.Service)
			}
		}
		if len(x.Command) == 0 {
			add("console.exec.command is required")
		}
		for i, c := range x.Command {
			if _, err := template.New("x").Funcs(funcs).Parse(c); err != nil {
				add("console.exec.command[%d]: template: %v", i, err)
			}
		}
	}
	if d.Console.Say != "" {
		if _, err := template.New("s").Funcs(funcs).Parse(d.Console.Say); err != nil {
			add("console.say: template: %v", err)
		}
	}
	for i, bp := range d.Backup.Paths {
		if err := checkRelPath(bp); err != nil {
			add("backup.paths[%d]: %v", i, err)
		}
	}
	if d.Container != nil && d.Container.Image == "" {
		add("container.image is required when container is set")
	}
	if d.LinuxGSM != nil {
		switch d.LinuxGSM.Status {
		case "upstream", "contributed", "missing":
		default:
			add("linuxgsm.status must be upstream, contributed or missing")
		}
	}

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

// SupportsOS reports whether the service has a native build for an OS.
func (d *Definition) SupportsOS(goos string) bool {
	for _, p := range d.Platforms {
		if p == goos {
			return true
		}
	}
	return false
}

// Setting returns the setting with the given key.
func (d *Definition) Setting(key string) (Setting, bool) {
	for _, s := range d.Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Port returns the port with the given name.
func (d *Definition) Port(name string) (Port, bool) {
	for _, p := range d.Ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// AppliesTo reports whether an OS list (empty = all) includes goos.
func AppliesTo(list []string, goos string) bool {
	if len(list) == 0 {
		return true
	}
	for _, o := range list {
		if o == goos {
			return true
		}
	}
	return false
}

// checkRelPath rejects absolute paths and anything that climbs out of the
// instance directory. Definitions use forward slashes on every OS.
func checkRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("%q: use forward slashes", p)
	}
	if path.IsAbs(p) || (len(p) >= 2 && p[1] == ':') {
		return fmt.Errorf("%q: must be relative to the instance directory", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q: must stay inside the instance directory", p)
	}
	return nil
}

func isKnownOS(s string) bool {
	for _, o := range KnownOS {
		if o == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefaultString renders the default as the raw string a user would type.
// Defaults decoded from JSON arrive as float64, so whole numbers are
// printed without an exponent or decimal point.
func (s Setting) DefaultString() string {
	if s.Default == nil {
		return ""
	}
	if n, ok := wholeNumber(s.Default); ok {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprint(s.Default)
}

func wholeNumber(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t), true
		}
	}
	return 0, false
}

// ArchiveURL reports whether a URL names a supported archive type.
func ArchiveURL(u string) bool {
	for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".tar.xz", ".txz"} {
		if strings.HasSuffix(u, ext) {
			return true
		}
	}
	return false
}

// Requires lists the host programs a run entry needs, for install checks.
func (r Run) Requires() []Launcher {
	var out []Launcher
	for _, v := range r.Via {
		if l, ok := Launchers[v]; ok {
			out = append(out, l)
		}
	}
	return out
}
