package app

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/fetch"
	"github.com/jonsjava/anyserve/internal/steps"
)

// DefaultBackupKeep is how many backups are kept when an instance doesn't
// say otherwise.
const DefaultBackupKeep = 10

// Backup is one backup file.
type Backup struct {
	Path    string
	Created time.Time
	Size    int64
}

func (a *App) backupDir(name string) string { return filepath.Join(a.Dirs.Backups(), name) }

// Backups lists an instance's backups, newest first.
func (a *App) Backups(name string) ([]Backup, error) {
	entries, err := os.ReadDir(a.backupDir(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{Path: filepath.Join(a.backupDir(name), e.Name()), Created: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path > out[j].Path })
	return out, nil
}

// MakeBackup archives the definition's backup paths. If the server is
// running and can save on command, it's told to save first.
func (a *App) MakeBackup(name string) (Backup, error) {
	in, d, t, err := a.load(name)
	if err != nil {
		return Backup{}, err
	}
	if len(d.Backup.Paths) == 0 {
		return Backup{}, fmt.Errorf("%s's definition doesn't say what to back up", d.Name)
	}
	state := t.Status(in).State
	// Anything but "stopped" may have a container (a database) still
	// writing, including "failed" and "partly running" stacks.
	if d.Backup.StopFirst && state != "stopped" {
		a.Log("Stopping %s for a consistent backup", in.Name)
		if err := t.Stop(in); err != nil {
			return Backup{}, err
		}
		// Start it again only if it was up. A failed stack stays down, so
		// a restore (which backs up first) never runs against live data.
		if state == "running" || state == "partly running" || state == "starting" {
			defer func() {
				a.Log("Starting %s again", in.Name)
				if err := t.Start(in); err != nil {
					a.Log("Couldn't start %s again: %v", in.Name, err)
				}
			}()
		}
	} else if state == "running" && d.Console.Save != "" {
		if err := a.sendLine(in, d, d.Console.Save); err != nil {
			a.Log("Couldn't ask the server to save first: %v", err)
		} else {
			a.Log("Asked the server to save; waiting 10 seconds")
			time.Sleep(10 * time.Second)
		}
	}
	if err := os.MkdirAll(a.backupDir(name), 0o700); err != nil {
		return Backup{}, err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	p := filepath.Join(a.backupDir(name), fmt.Sprintf("%s-%s.tar.gz", name, stamp))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Backup{}, err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(p)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	count := 0
	for _, rel := range d.Backup.Paths {
		src, err := steps.Resolve(in.ServerDir, rel)
		if err != nil {
			return Backup{}, err
		}
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err = filepath.WalkDir(src, func(fp string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			name, _ := filepath.Rel(in.ServerDir, fp)
			h, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			h.Name = filepath.ToSlash(name)
			if e.IsDir() {
				h.Name += "/"
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				rf, err := os.Open(fp)
				if err != nil {
					return err
				}
				_, err = io.Copy(tw, rf)
				rf.Close()
				if err != nil {
					return err
				}
				count++
			}
			return nil
		})
		if err != nil {
			return Backup{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Backup{}, err
	}
	if err := gz.Close(); err != nil {
		return Backup{}, err
	}
	if err := f.Close(); err != nil {
		return Backup{}, err
	}
	if count == 0 {
		return Backup{}, errors.New("nothing to back up yet (has the server created its world?)")
	}
	ok = true
	info, _ := os.Stat(p)
	a.Log("Backed up %d files to %s", count, p)
	a.prune(name, in.BackupKeep)
	return Backup{Path: p, Created: info.ModTime(), Size: info.Size()}, nil
}

func (a *App) prune(name string, keep int) {
	if keep <= 0 {
		keep = DefaultBackupKeep
	}
	list, err := a.Backups(name)
	if err != nil {
		return
	}
	for i, b := range list {
		if i >= keep {
			if os.Remove(b.Path) == nil {
				a.Log("Removed old backup %s", filepath.Base(b.Path))
			}
		}
	}
}

// Restore unpacks a backup over the server files. The server must be
// stopped, and a safety backup of the current state is taken first.
func (a *App) Restore(name, file string) error {
	in, d, t, err := a.load(name)
	if err != nil {
		return err
	}
	if st := t.Status(in).State; st == "running" || st == "starting" || st == "partly running" {
		return fmt.Errorf("stop %s first (`anyserve stop %s`)", name, name)
	}
	if _, err := os.Stat(file); err != nil {
		// Allow just the file name of one of this instance's backups.
		alt := filepath.Join(a.backupDir(name), filepath.Base(file))
		if _, err2 := os.Stat(alt); err2 != nil {
			return err
		}
		file = alt
	}
	_, safetyErr := a.MakeBackup(name)
	if safetyErr != nil {
		a.Log("No safety backup taken: %v", safetyErr)
	}
	if d.Backup.StopFirst {
		// Databases must be restored exactly: newer files left beside the
		// restored ones (log segments, new tables) can corrupt them. So the
		// backed-up paths are replaced, not merged, and only once the
		// current state is safely saved.
		if safetyErr != nil {
			return fmt.Errorf("not restoring, because the current data couldn't be backed up first: %w", safetyErr)
		}
		for _, rel := range d.Backup.Paths {
			p, err := steps.Resolve(in.ServerDir, rel)
			if err != nil {
				return err
			}
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	n, err := fetch.Extract(file, "", in.ServerDir)
	if err != nil {
		return err
	}
	a.Log("Restored %d files from %s", n, filepath.Base(file))
	return nil
}
