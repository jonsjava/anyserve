// Package steps runs a definition's built-in install steps and writes its
// config files. Every path is resolved inside the server directory.
package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/spec"
)

// Options carries host details some steps need.
type Options struct {
	// SteamCMDDir is where Anyserve's SteamCMD lives, for steam_sdk.
	SteamCMDDir string
}

// Apply runs the steps that apply to goos, in order.
func Apply(d *spec.Definition, dir, goos string, opts Options) error {
	for i, s := range d.Steps {
		if !spec.AppliesTo(s.OS, goos) {
			continue
		}
		if err := apply(s, dir, opts); err != nil {
			return fmt.Errorf("%s step %d (%s): %w", d.ID, i+1, s.Type, err)
		}
	}
	return nil
}

func apply(s spec.Step, dir string, opts Options) error {
	switch s.Type {
	case "exec":
		// Runs later, in RunExec, once config files are written.
		return nil
	case "steam_sdk":
		if runtime.GOOS != "linux" {
			return nil
		}
		copied := 0
		for _, bits := range []string{"32", "64"} {
			src := filepath.Join(opts.SteamCMDDir, "linux"+bits, "steamclient.so")
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst, err := Resolve(dir, ".steam/sdk"+bits+"/steamclient.so")
			if err != nil {
				return err
			}
			if err := copyFile(src, dst); err != nil {
				return err
			}
			copied++
		}
		if copied == 0 {
			return fmt.Errorf("SteamCMD's steamclient.so wasn't found in %s", opts.SteamCMDDir)
		}
		return nil
	case "steamcmd_file":
		from, err := Resolve(opts.SteamCMDDir, s.From)
		if err != nil {
			return err
		}
		to, err := Resolve(dir, s.To)
		if err != nil {
			return err
		}
		return copyFile(from, to)
	case "copy_dir_if_missing":
		from, to, err := pair(dir, s.From, s.To)
		if err != nil {
			return err
		}
		if exists(to) {
			return nil
		}
		return copyDir(from, to)
	case "copy_file_if_missing":
		from, to, err := pair(dir, s.From, s.To)
		if err != nil {
			return err
		}
		if exists(to) {
			return nil
		}
		return copyFile(from, to)
	case "copy_file":
		from, to, err := pair(dir, s.From, s.To)
		if err != nil {
			return err
		}
		return copyFile(from, to)
	case "touch":
		p, err := Resolve(dir, s.Path)
		if err != nil {
			return err
		}
		if exists(p) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, nil, 0o644)
	case "mkdir":
		p, err := Resolve(dir, s.Path)
		if err != nil {
			return err
		}
		return os.MkdirAll(p, 0o755)
	case "chmod_exec":
		if runtime.GOOS == "windows" {
			return nil
		}
		p, err := Resolve(dir, s.Path)
		if err != nil {
			return err
		}
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		return os.Chmod(p, st.Mode()|0o111)
	}
	return fmt.Errorf("unknown step type %q", s.Type)
}

// WriteFiles writes the definition's config files. Templated files without
// overwrite are only written when missing, so hand edits survive. Files with
// set have just those values changed every time; if such a file doesn't
// exist yet (many games create theirs on first start) and has no template,
// it is listed in pending and tried again on the next start.
func WriteFiles(d *spec.Definition, dir string, ctx spec.Context) (written, pending []string, err error) {
	for _, f := range d.Files {
		if !spec.AppliesTo(f.OS, ctx.OS) {
			continue
		}
		p, err := Resolve(dir, f.Path)
		if err != nil {
			return written, pending, err
		}
		existing, readErr := os.ReadFile(p)
		have := readErr == nil
		var body string
		switch {
		case f.Template != "" && (!have || (f.Overwrite && len(f.Set) == 0)):
			if body, err = spec.Render(f.Path, f.Template, ctx); err != nil {
				return written, pending, err
			}
		case have:
			body = string(existing)
		case len(f.Set) > 0:
			pending = append(pending, f.Path)
			continue
		default:
			continue
		}
		if len(f.Set) > 0 {
			vals := map[string]string{}
			for k, v := range f.Set {
				if vals[k], err = spec.Render(f.Path+"["+k+"]", v, ctx); err != nil {
					return written, pending, err
				}
			}
			if body, err = patchFile(f.Format, body, vals); err != nil {
				return written, pending, fmt.Errorf("%s: %w", f.Path, err)
			}
		}
		if have && body == string(existing) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return written, pending, err
		}
		// Config files often hold passwords.
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return written, pending, err
		}
		written = append(written, f.Path)
	}
	return written, pending, nil
}

// Resolve turns a definition's relative path into a host path inside dir,
// refusing anything that would escape it (including through symlinks).
func Resolve(dir, rel string) (string, error) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	r, err := filepath.Rel(dir, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes the server directory", rel)
	}
	// Check the deepest existing ancestor isn't a symlink out of dir.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	for q := p; ; q = filepath.Dir(q) {
		if real, err := filepath.EvalSymlinks(q); err == nil {
			rr, err := filepath.Rel(realDir, real)
			if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("%q points outside the server directory", rel)
			}
			break
		}
		if q == dir || q == filepath.Dir(q) {
			break
		}
	}
	return p, nil
}

func pair(dir, from, to string) (string, string, error) {
	f, err := Resolve(dir, from)
	if err != nil {
		return "", "", err
	}
	t, err := Resolve(dir, to)
	if err != nil {
		return "", "", err
	}
	return f, t, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", from)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(from, to string) error {
	st, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", from)
	}
	return filepath.WalkDir(from, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		switch {
		case e.IsDir():
			return os.MkdirAll(dst, 0o755)
		case e.Type()&fs.ModeSymlink != 0:
			return nil // never follow links while copying
		case e.Type().IsRegular():
			return copyFile(p, dst)
		}
		return nil
	})
}

// ErrNotInstalled means the server files aren't there.
var ErrNotInstalled = errors.New("server files are missing; run `anyserve update` to reinstall them")

// RunExec runs the definition's exec steps that apply to goos: each runs the
// server's own command once, from the server folder with HOME set to it,
// unless its creates path already exists.
func RunExec(ctx context.Context, d *spec.Definition, dir, goos string, tctx spec.Context, out io.Writer) error {
	for i, s := range d.Steps {
		if s.Type != "exec" || !spec.AppliesTo(s.OS, goos) {
			continue
		}
		where := fmt.Sprintf("%s step %d (exec)", d.ID, i+1)
		creates, err := Resolve(dir, s.Creates)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if exists(creates) {
			continue
		}
		cmdPath, err := Resolve(dir, s.Command)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		var args []string
		for j, a := range s.Args {
			v, err := spec.Render(fmt.Sprintf("steps[%d].args[%d]", i, j), a, tctx)
			if err != nil {
				return err
			}
			if v != "" {
				args = append(args, v)
			}
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		cmd := exec.CommandContext(rctx, cmdPath, args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = out, out
		cmd.Env = os.Environ()
		if runtime.GOOS != "windows" {
			cmd.Env = append(cmd.Env, "HOME="+dir)
		}
		err = cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %s: %w", where, s.Command, err)
		}
		if !exists(creates) {
			return fmt.Errorf("%s: %s finished but didn't create %s", where, s.Command, s.Creates)
		}
	}
	return nil
}
