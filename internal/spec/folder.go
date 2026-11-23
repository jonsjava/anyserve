package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// blockedFolders are host folders a folder setting may never point at or
// into: system folders, and secrets kept in the home folder. A definition
// can only ask for a folder; the user chooses it, and this keeps an honest
// mistake (or a misleading prompt) from handing a container the keys to the
// machine.
var blockedFolders = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32",
	"/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var/lib/docker",
	"/var/run", "/System", "/Library", "/private/etc", "/private/var/run",
}

// blockedHomeFolders are folders under the home folder that hold keys,
// credentials or other programs' state.
var blockedHomeFolders = []string{
	".ssh", ".gnupg", ".config", ".local", ".aws", ".kube", ".docker",
	".password-store", ".mozilla", ".steam", "snap", "Library",
}

// HostFolder checks a folder setting's value: an existing folder on this
// machine, given as an absolute path or starting with ~. It returns the
// cleaned absolute path with symlinks resolved.
func HostFolder(v string) (string, error) {
	v = strings.TrimSpace(v)
	home, _ := os.UserHomeDir()
	if v == "~" || strings.HasPrefix(v, "~/") {
		if home == "" {
			return "", fmt.Errorf("can't find your home folder to expand %q", v)
		}
		v = filepath.Join(home, strings.TrimPrefix(v, "~"))
	}
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("%q must be an absolute path (or start with ~/)", v)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(v))
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", v, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s isn't a folder", v)
	}
	if err := checkHostFolder(real, home); err != nil {
		return "", err
	}
	return real, nil
}

func checkHostFolder(p, home string) error {
	if runtime.GOOS == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
		home = strings.ReplaceAll(home, `\`, "/")
	}
	if filepath.Dir(p) == p || p == "/" {
		return fmt.Errorf("%s: sharing a whole drive isn't allowed; pick the folder with the files", p)
	}
	for _, b := range blockedFolders {
		if within(p, b) {
			return fmt.Errorf("%s is a system folder; pick a folder of your own files", p)
		}
	}
	if home != "" {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if p == home {
			return fmt.Errorf("%s is your whole home folder, which holds keys and passwords; pick the folder with the files", p)
		}
		for _, b := range blockedHomeFolders {
			if within(p, filepath.Join(home, b)) {
				return fmt.Errorf("%s holds settings or keys for other programs; pick a folder of your own files", p)
			}
		}
	}
	return nil
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
