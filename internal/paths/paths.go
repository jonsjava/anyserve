// Package paths decides where Anyserve keeps its files on each OS.
package paths

import (
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
