// Package paths decides where Anyserve keeps its files on each OS.
package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Dirs are Anyserve's working directories.
type Dirs struct {
	// Config holds settings such as the registry list.
	Config string
	// Data holds instances, backups and tools like SteamCMD.
	Data string
	// Cache holds registry indexes and downloads that can be refetched.
	Cache string
}

// Instances is where each instance gets its own directory.
func (d Dirs) Instances() string { return filepath.Join(d.Data, "instances") }

// Backups is where backups are written unless an instance overrides it.
func (d Dirs) Backups() string { return filepath.Join(d.Data, "backups") }

// Tools is where Anyserve installs helpers such as SteamCMD.
func (d Dirs) Tools() string { return filepath.Join(d.Data, "tools") }

// Socket is the local API's unix socket: in the data folder, unless that
// path is too long for a unix socket (about 100 bytes), in which case it's
// in a private per-user folder under the runtime directory.
func (d Dirs) Socket() string {
	p := filepath.Join(d.Data, "anyserve.sock")
	if len(p) <= 100 {
		return p
	}
	sum := sha256.Sum256([]byte(d.Data))
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" || runtime.GOOS != "linux" {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("anyserve-%d", os.Getuid()), fmt.Sprintf("api-%x.sock", sum[:4]))
}

// AuditLog records every change made through the API.
func (d Dirs) AuditLog() string { return filepath.Join(d.Data, "audit.log") }

// Registries is the verified registry index cache.
func (d Dirs) Registries() string { return filepath.Join(d.Cache, "registries") }

// Downloads caches downloaded archives by checksum.
func (d Dirs) Downloads() string { return filepath.Join(d.Cache, "downloads") }

// Default returns the per-user directories for this OS. Setting
// ANYSERVE_HOME puts everything under one directory instead, which tests
// and portable installs use.
func Default() (Dirs, error) {
	if h := os.Getenv("ANYSERVE_HOME"); h != "" {
		h, err := filepath.Abs(h)
		if err != nil {
			return Dirs{}, err
		}
		return Dirs{Config: filepath.Join(h, "config"), Data: filepath.Join(h, "data"), Cache: filepath.Join(h, "cache")}, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Dirs{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Dirs{}, err
	}
	var data string
	switch runtime.GOOS {
	case "windows":
		data = os.Getenv("LOCALAPPDATA")
		if data == "" {
			data = cfg
		}
	case "darwin":
		// ~/Library/Application Support, same as UserConfigDir.
		data = cfg
	default:
		data = os.Getenv("XDG_DATA_HOME")
		if data == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return Dirs{}, err
			}
			data = filepath.Join(home, ".local", "share")
		}
	}
	return Dirs{
		Config: filepath.Join(cfg, "anyserve"),
		Data:   filepath.Join(data, "anyserve"),
		Cache:  filepath.Join(cache, "anyserve"),
	}, nil
}
