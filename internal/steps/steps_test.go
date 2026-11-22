package steps

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jonsjava/anyserve/internal/spec"
)

func TestResolveRefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need admin rights on Windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(dir, "sneaky/file"); err == nil {
		t.Fatal("a path through a symlink to outside the server directory must be refused")
	}
	if _, err := Resolve(dir, "fine/file"); err != nil {
		t.Fatalf("normal path refused: %v", err)
	}
}

func TestApplyWurmStyleSteps(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "dist", "Creative", "db"), 0o755)
	os.WriteFile(filepath.Join(dir, "dist", "Creative", "db", "w.db"), []byte("world"), 0o644)
	os.MkdirAll(filepath.Join(dir, "linux64"), 0o755)
	os.WriteFile(filepath.Join(dir, "linux64", "steamclient.so"), []byte("lib"), 0o644)
	os.WriteFile(filepath.Join(dir, "Launcher"), []byte("#!/bin/sh"), 0o644)

	d := &spec.Definition{ID: "t", Steps: []spec.Step{
		{Type: "copy_dir_if_missing", From: "dist/Creative", To: "Creative"},
		{Type: "touch", Path: "Creative/gamedir"},
		{Type: "copy_file_if_missing", From: "linux64/steamclient.so", To: "nativelibs/steamclient.so", OS: []string{"linux"}},
		{Type: "chmod_exec", Path: "Launcher"},
		{Type: "mkdir", Path: "worlds"},
	}}
	if err := Apply(d, dir, "linux", Options{}); err != nil {
		t.Fatal(err)
	}
	// A second run must not overwrite the copied world.
	os.WriteFile(filepath.Join(dir, "Creative", "db", "w.db"), []byte("played"), 0o644)
	if err := Apply(d, dir, "linux", Options{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Creative", "db", "w.db")); string(b) != "played" {
		t.Errorf("world was overwritten on re-run: %q", b)
	}
	for _, p := range []string{"Creative/gamedir", "nativelibs/steamclient.so", "worlds"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "Launcher"))
		if st.Mode().Perm()&0o100 == 0 {
			t.Error("chmod_exec didn't set the executable bit")
		}
	}
}

func TestWriteFilesKeepsHandEdits(t *testing.T) {
	dir := t.TempDir()
	d := &spec.Definition{Files: []spec.File{
		{Path: "managed.cfg", Template: "port={{.Ports.game}}", Overwrite: true},
		{Path: "user.cfg", Template: "default"},
	}}
	ctx := spec.Context{OS: "linux", Ports: map[string]int{"game": 1}}
	if _, _, err := WriteFiles(d, dir, ctx); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "user.cfg"), []byte("edited"), 0o644)
	ctx.Ports["game"] = 2
	if _, _, err := WriteFiles(d, dir, ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "managed.cfg")); string(b) != "port=2" {
		t.Errorf("managed.cfg = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "user.cfg")); string(b) != "edited" {
		t.Errorf("hand edit lost: %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "managed.cfg"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("config files can hold passwords and should be 0600, got %v", st.Mode().Perm())
		}
	}
}
