// Package instance stores installed servers ("instances") on disk.
//
// Each instance has a directory holding instance.json (what was installed
// and with which settings), a copy of the definition it was installed from,
// the server files, and logs.
package instance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"time"

	"github.com/jonsjava/anyserve/internal/spec"
)

// Instance is one installed server.
type Instance struct {
	Name     string `json:"name"`
	Service  string `json:"service"`
	Registry string `json:"registry"`
	// DefinitionVersion is the definition version last applied.
	DefinitionVersion string `json:"definition_version"`
	Target            string `json:"target"`
	// ServerDir holds the server files. It defaults to <dir>/server.
	ServerDir string `json:"server_dir"`
	// Settings holds raw values as the user gave them (or as generated),
	// so they can be re-resolved against newer definitions.
	Settings  map[string]string `json:"settings"`
	Ports     map[string]int    `json:"ports"`
	Installed time.Time         `json:"installed"`
	Updated   time.Time         `json:"updated"`
	// BackupKeep is how many backups to keep. 0 means the default.
	BackupKeep int `json:"backup_keep,omitempty"`

	// Dir is the instance's own directory. Not stored.
	Dir string `json:"-"`
}

// NameRe is the allowed shape of instance names. Names become directory,
// service and container names, so they're kept conservative.
var NameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidateName checks an instance name.
func ValidateName(name string) error {
	if !NameRe.MatchString(name) {
		return fmt.Errorf("instance name %q must be 1-40 lowercase letters, digits and dashes, starting with a letter or digit", name)
	}
	return nil
}

// Store keeps instances under one directory.
type Store struct{ Root string }

// Path returns an instance's directory.
func (s Store) Path(name string) string { return filepath.Join(s.Root, name) }

// Exists reports whether an instance exists.
func (s Store) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(s.Path(name), "instance.json"))
	return err == nil
}

// ErrNotFound means there's no instance with that name.
var ErrNotFound = errors.New("no such instance")

// Load reads one instance and the definition it was installed from.
func (s Store) Load(name string) (*Instance, *spec.Definition, error) {
	if err := ValidateName(name); err != nil {
		return nil, nil, err
	}
	dir := s.Path(name)
	b, err := os.ReadFile(filepath.Join(dir, "instance.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w %q (see `anyserve list`)", ErrNotFound, name)
	}
	if err != nil {
		return nil, nil, err
	}
	var in Instance
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	in.Dir = dir
	db, err := os.ReadFile(filepath.Join(dir, "definition.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: definition snapshot: %w", name, err)
	}
	var d spec.Definition
	if err := json.Unmarshal(db, &d); err != nil {
		return nil, nil, fmt.Errorf("%s: definition snapshot: %w", name, err)
	}
	if err := d.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%s: definition snapshot: %w", name, err)
	}
	return &in, &d, nil
}

// Save writes an instance and its definition snapshot. instance.json can
// hold passwords, so it's readable only by the owner.
func (s Store) Save(in *Instance, d *spec.Definition) error {
	if err := ValidateName(in.Name); err != nil {
		return err
	}
	in.Dir = s.Path(in.Name)
	if err := os.MkdirAll(in.Dir, 0o700); err != nil {
		return err
	}
	db, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(in.Dir, "definition.json"), db, 0o644); err != nil {
		return err
	}
	ib, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(in.Dir, "instance.json"), ib, 0o600)
}

// List returns every instance, sorted by name. Broken instances are
// returned with their error so `list` can still show them.
func (s Store) List() ([]*Instance, map[string]error, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []*Instance
	broken := map[string]error{}
	for _, e := range entries {
		if !e.IsDir() || !NameRe.MatchString(e.Name()) || !s.Exists(e.Name()) {
			continue
		}
		in, _, err := s.Load(e.Name())
		if err != nil {
			broken[e.Name()] = err
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, broken, nil
}

// Delete removes an instance directory, including server files stored
// inside it. Server files in a custom ServerDir are left alone.
func (s Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return os.RemoveAll(s.Path(name))
}

// LogDir is where the supervisor writes server logs.
func (in *Instance) LogDir() string { return filepath.Join(in.Dir, "logs") }

// maxSocketPath stays under the smallest Unix socket path limit (104 bytes
// on macOS, 108 on Linux).
const maxSocketPath = 100

// ConsoleSocket is the path of the supervisor's console socket. It lives in
// the instance directory unless that path is too long for a Unix socket, in
// which case it moves to a short private per-user directory.
func (in *Instance) ConsoleSocket() string {
	p := filepath.Join(in.Dir, "console.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(in.Dir))
	file := fmt.Sprintf("%s-%x.sock", in.Name, sum[:4])
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" || runtime.GOOS != "linux" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, fmt.Sprintf("anyserve-%d", os.Getuid()))
	// Private to this user, so nobody else can reach the console.
	os.MkdirAll(dir, 0o700)
	os.Chmod(dir, 0o700)
	return filepath.Join(dir, file)
}

// Context builds the template context for this instance.
func (in *Instance) Context(d *spec.Definition, goos string) (spec.Context, error) {
	settings, err := d.ResolveSettings(in.Settings)
	if err != nil {
		return spec.Context{}, err
	}
	ports, err := d.ResolvePorts(in.Ports)
	if err != nil {
		return spec.Context{}, err
	}
	return spec.Context{Name: in.Name, Dir: in.ServerDir, OS: goos, Settings: settings, Ports: ports}, nil
}

func writeFile(p string, data []byte, mode os.FileMode) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
