package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostFolder(t *testing.T) {
	dir := t.TempDir()
	got, err := HostFolder(dir)
	if err != nil {
		t.Fatalf("temp folder refused: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(dir); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "etc-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{
		"", "relative/path", "/", "/etc", "/etc/ssl", "/proc", "/var/run",
		"/usr/share", file, filepath.Join(dir, "missing"), link,
		home, "~", "~/.ssh", "~/.config",
	} {
		if bad == "" && home == "" {
			continue
		}
		if _, err := HostFolder(bad); err == nil {
			t.Errorf("HostFolder(%q) should be refused", bad)
		}
	}
}

func TestFolderSettingConvert(t *testing.T) {
	s := Setting{Key: "media", Type: "folder"}
	if _, err := s.Convert(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("empty required folder: %v", err)
	}
	s.Optional = true
	if v, err := s.Convert(""); err != nil || v != "" {
		t.Errorf("empty optional folder: %v %v", v, err)
	}
	if _, err := s.Convert("/etc"); err == nil {
		t.Error("/etc accepted")
	}
}
