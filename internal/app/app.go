// Package app ties the pieces together: the CLI and the wizard call it to
// install, update, run and manage instances.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonsjava/anyserve/internal/version"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/registry"
	"github.com/jonsjava/anyserve/internal/spec"
)

// App is the shared state for one command.
type App struct {
	Dirs  paths.Dirs
	Store instance.Store
	// Out is for normal output, Log for progress messages.
	Out io.Writer
	Log func(format string, a ...any)
}

// New sets up an App with the default directories.
func New(out io.Writer) (*App, error) {
	d, err := paths.Default()
	if err != nil {
		return nil, err
	}
	a := &App{Dirs: d, Store: instance.Store{Root: d.Instances()}, Out: out}
	a.Log = func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
	return a, nil
}

// Config is the user's Anyserve settings file.
type Config struct {
	// Registries are extra registries on top of the built-in main one.
	Registries []registry.Source `json:"registries,omitempty"`
	// SteamUser is the account `anyserve steam login` signed in with, for
	// servers that can't download anonymously. SteamCMD keeps the session;
	// the password is never stored here.
	SteamUser string `json:"steam_user,omitempty"`
	// NotifyURL receives a plain-text POST summarising scheduled updates
	// (an ntfy topic URL works as is).
	NotifyURL string `json:"notify_url,omitempty"`
	// Users may manage instances remotely (`anyserve users`).
	Users []User `json:"users,omitempty"`
	// Remote is the built-in SSH server's settings (`anyserve remote`).
	Remote RemoteConfig `json:"remote,omitempty"`
	// HTTPS is the reverse proxy for apps' domains (`anyserve https`).
	HTTPS HTTPSConfig `json:"https,omitempty"`
	// Router shares the Minecraft port between servers by hostname.
	Router RouterConfig `json:"router,omitempty"`
	// Hosts are other machines this one manages (`anyserve remote add`).
	Hosts []RemoteHost `json:"hosts,omitempty"`
	// Fleet links this machine to others (`anyserve fleet`).
	Fleet FleetConfig `json:"fleet,omitempty"`
	// Reserved are port ranges installs leave alone (`anyserve reserve`).
	Reserved []PortReservation `json:"reserved_ports,omitempty"`
}

func (a *App) configPath() string { return filepath.Join(a.Dirs.Config, "config.json") }

// LoadConfig reads the config file, returning an empty config if missing.
func (a *App) LoadConfig() (*Config, error) {
	b, err := os.ReadFile(a.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", a.configPath(), err)
	}
	return &c, nil
}

// SaveConfig writes the config file.
func (a *App) SaveConfig(c *Config) error {
	if err := os.MkdirAll(a.Dirs.Config, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.configPath(), append(b, '\n'), 0o644)
}

// Sources lists every configured registry, main first.
func (a *App) Sources() ([]registry.Source, error) {
	c, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	return append([]registry.Source{registry.Main()}, c.Registries...), nil
}

// Entry is one service in the combined catalog.
type Entry struct {
	Registry string
	Def      *spec.Definition
}

// Catalog loads every registry from the verified cache. The main registry
// falls back to the snapshot built into the binary. Registries that can't
// be loaded are reported as warnings, not errors.
func (a *App) Catalog() ([]Entry, []string, error) {
	entries, warnings, _, err := a.catalog()
	return entries, warnings, err
}

// skippedEntry is a definition a registry offers that this version of
// Anyserve can't use.
type skippedEntry struct {
	Registry string
	registry.Skipped
}

func (a *App) catalog() ([]Entry, []string, []skippedEntry, error) {
	srcs, err := a.Sources()
	if err != nil {
		return nil, nil, nil, err
	}
	var skipped []skippedEntry
	cache := registry.Cache{Dir: a.Dirs.Registries()}
	var out []Entry
	var warnings []string
	for _, src := range srcs {
		ix, err := cache.Load(src)
		if err != nil && src.Name == registry.MainName {
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("cached main registry unusable (%v); using the built-in copy", err))
			}
			ix, err = registry.Embedded()
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("registry %s hasn't been downloaded yet; run `anyserve refresh`", src.Name))
			} else {
				warnings = append(warnings, fmt.Sprintf("registry %s: %v", src.Name, err))
			}
			continue
		}
		for _, d := range ix.Services {
			out = append(out, Entry{Registry: src.Name, Def: d})
		}
		if len(ix.Skipped) > 0 {
			ids := make([]string, len(ix.Skipped))
			for i, s := range ix.Skipped {
				ids[i] = s.ID
				skipped = append(skipped, skippedEntry{Registry: src.Name, Skipped: s})
			}
			what := fmt.Sprintf("%d services", len(ids))
			if len(ids) == 1 {
				what = "1 service"
			}
			warnings = append(warnings, fmt.Sprintf("registry %s has %s this version of Anyserve (%s) can't use: %s. %s", src.Name, what, version.Current(), strings.Join(ids, ", "), UpdateHint))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Def.ID < out[j].Def.ID })
	return out, warnings, skipped, nil
}

// NeedsNewerError reports a service the registry offers that this version
// of Anyserve can't use.
type NeedsNewerError struct {
	ID, Registry, Reason string
}

func (e *NeedsNewerError) Error() string {
	return fmt.Sprintf("%s (registry %s) %s. Update Anyserve to install it: go install github.com/jonsjava/anyserve/cmd/anyserve@latest", e.ID, e.Registry, e.Reason)
}

// UpdateHint tells people how to get a newer Anyserve.
const UpdateHint = "Update Anyserve to get them: go install github.com/jonsjava/anyserve/cmd/anyserve@latest"

// Find looks a service up by id, optionally as "registry/id".
func (a *App) Find(ref string) (Entry, error) {
	reg, id := "", ref
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		reg, id = ref[:i], ref[i+1:]
	}
	entries, _, skipped, err := a.catalog()
	if err != nil {
		return Entry{}, err
	}
	var matches []Entry
	for _, e := range entries {
		if e.Def.ID == id && (reg == "" || e.Registry == reg) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		for _, s := range skipped {
			if s.ID == id && (reg == "" || s.Registry == reg) {
				return Entry{}, &NeedsNewerError{ID: id, Registry: s.Registry, Reason: s.Reason}
			}
		}
		return Entry{}, fmt.Errorf("no service %q in any registry (see `anyserve available`)", ref)
	case 1:
		return matches[0], nil
	}
	// The main registry wins ties unless the user picked one.
	for _, m := range matches {
		if m.Registry == registry.MainName {
			return m, nil
		}
	}
	return Entry{}, fmt.Errorf("%q is in several registries; use registry/%s", id, id)
}

// Refresh downloads and verifies every registry, caching the results.
func (a *App) Refresh(ctx context.Context) error {
	srcs, err := a.Sources()
	if err != nil {
		return err
	}
	cache := registry.Cache{Dir: a.Dirs.Registries()}
	var errs []string
	for _, src := range srcs {
		index, sig, err := registry.Fetch(ctx, src)
		if err != nil {
			if src.Name == registry.MainName {
				// Offline or unreachable: the built-in copy still works.
				a.Log("Main registry: %v; using the copy built into anyserve", err)
				continue
			}
			errs = append(errs, err.Error())
			continue
		}
		if err := cache.Save(src.Name, index, sig); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ix, _ := registry.ParseIndex(index)
		if n := len(ix.Skipped); n > 0 {
			a.Log("Registry %s: %d services, and %d that need a newer Anyserve", src.Name, len(ix.Services), n)
		} else {
			a.Log("Registry %s: %d services", src.Name, len(ix.Services))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// AddRegistry adds and fetches an extra registry.
func (a *App) AddRegistry(ctx context.Context, src registry.Source) error {
	if err := instance.ValidateName(src.Name); err != nil {
		return fmt.Errorf("registry name: %w", err)
	}
	if src.Name == registry.MainName {
		return errors.New(`"main" is the built-in registry`)
	}
	if _, err := registry.ParsePublicKey(src.PublicKey); err != nil {
		return err
	}
	c, err := a.LoadConfig()
	if err != nil {
		return err
	}
	for _, r := range c.Registries {
		if r.Name == src.Name {
			return fmt.Errorf("registry %q already exists", src.Name)
		}
	}
	index, sig, err := registry.Fetch(ctx, src)
	if err != nil {
		return err
	}
	if err := (registry.Cache{Dir: a.Dirs.Registries()}).Save(src.Name, index, sig); err != nil {
		return err
	}
	c.Registries = append(c.Registries, src)
	return a.SaveConfig(c)
}

// RemoveRegistry removes an extra registry.
func (a *App) RemoveRegistry(name string) error {
	c, err := a.LoadConfig()
	if err != nil {
		return err
	}
	kept := c.Registries[:0]
	found := false
	for _, r := range c.Registries {
		if r.Name == name {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return fmt.Errorf("no registry %q", name)
	}
	c.Registries = kept
	os.RemoveAll(filepath.Join(a.Dirs.Registries(), name))
	return a.SaveConfig(c)
}
