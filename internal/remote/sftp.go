package remote

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/jonsjava/anyserve/internal/app"
)

// serveSFTP serves a virtual tree: "/" lists the instances the caller may
// manage, and "/<instance>/..." is that instance's server folder, opened
// as an os.Root so nothing (not "..", not a symlink) leads outside it.
func serveSFTP(ch io.ReadWriteCloser, a *app.App, c app.Caller, audit func(string, ...any), who string) {
	v := &vfs{a: a, c: c, roots: map[string]*os.Root{}, audit: audit, who: who}
	defer v.close()
	srv := sftp.NewRequestServer(ch, sftp.Handlers{FileGet: v, FilePut: v, FileCmd: v, FileList: v})
	srv.Serve()
	srv.Close()
}

type vfs struct {
	a     *app.App
	c     app.Caller
	mu    sync.Mutex
	roots map[string]*os.Root
	audit func(string, ...any)
	who   string
}

func (v *vfs) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range v.roots {
		r.Close()
	}
}

// split turns "/name/a/b" into ("name", "a/b"); "/" gives ("", ".").
func split(p string) (string, string) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		return "", "."
	}
	name, rel, _ := strings.Cut(p, "/")
	if rel == "" {
		rel = "."
	}
	return name, rel
}

// root returns the instance's root, checking the caller may manage it
// (again on every call, so a revoked grant applies at once).
func (v *vfs) root(name string) (*os.Root, error) {
	if name == "" || !v.a.Store.Exists(name) || !v.a.CanManage(v.c, name) {
		return nil, os.ErrNotExist
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if r, ok := v.roots[name]; ok {
		return r, nil
	}
	in, _, err := v.a.Store.Load(name)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(in.ServerDir)
	if err != nil {
		return nil, err
	}
	v.roots[name] = r
	return r, nil
}

func (v *vfs) resolve(p string) (*os.Root, string, error) {
	name, rel := split(p)
	r, err := v.root(name)
	return r, rel, err
}

func (v *vfs) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	r, rel, err := v.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	return r.Open(rel)
}

func (v *vfs) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	r, rel, err := v.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, fs.ErrPermission
	}
	flags := os.O_WRONLY | os.O_CREATE
	pf := req.Pflags()
	if pf.Trunc {
		flags |= os.O_TRUNC
	}
	if pf.Excl {
		flags |= os.O_EXCL
	}
	if pf.Append {
		flags |= os.O_APPEND
	}
	v.audit("%s wrote %s", v.who, req.Filepath)
	return r.OpenFile(rel, flags, 0o644)
}

func (v *vfs) Filecmd(req *sftp.Request) error {
	r, rel, err := v.resolve(req.Filepath)
	if err != nil {
		return err
	}
	switch req.Method {
	case "Setstat":
		a := req.Attributes()
		f := req.AttrFlags()
		if f.Permissions {
			if err := r.Chmod(rel, a.FileMode()&fs.ModePerm); err != nil {
				return err
			}
		}
		if f.Acmodtime {
			if err := r.Chtimes(rel, time.Unix(int64(a.Atime), 0), time.Unix(int64(a.Mtime), 0)); err != nil {
				return err
			}
		}
		return nil
	case "Rename", "PosixRename":
		toName, toRel := split(req.Target)
		fromName, _ := split(req.Filepath)
		if toName != fromName || rel == "." || toRel == "." {
			return fs.ErrPermission
		}
		v.audit("%s renamed %s to %s", v.who, req.Filepath, req.Target)
		return r.Rename(rel, toRel)
	case "Rmdir", "Remove":
		if rel == "." {
			return fs.ErrPermission
		}
		v.audit("%s removed %s", v.who, req.Filepath)
		return r.Remove(rel)
	case "Mkdir":
		return r.Mkdir(rel, 0o755)
	case "Symlink", "Link":
		// Links could point anywhere; os.Root would refuse to follow them
		// out, but there's no need to let users create them at all.
		return errors.New("links aren't supported")
	}
	return sftp.ErrSSHFxOpUnsupported
}

type listerAt []fs.FileInfo

func (l listerAt) ListAt(ls []fs.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// named renames a FileInfo, for an instance folder's own entry.
type named struct {
	fs.FileInfo
	name string
}

func (n named) Name() string { return n.name }

// dirInfo describes the virtual root.
type dirInfo struct{ name string }

func (d dirInfo) Name() string       { return d.name }
func (d dirInfo) Size() int64        { return 0 }
func (d dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (d dirInfo) ModTime() time.Time { return time.Time{} }
func (d dirInfo) IsDir() bool        { return true }
func (d dirInfo) Sys() any           { return nil }

func (v *vfs) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	name, rel := split(req.Filepath)
	if name == "" {
		switch req.Method {
		case "Stat", "Lstat":
			return listerAt{dirInfo{name: "/"}}, nil
		case "List":
			list, _, err := v.a.Store.List()
			if err != nil {
				return nil, err
			}
			var out listerAt
			for _, in := range list {
				if v.a.CanManage(v.c, in.Name) {
					if fi, err := os.Stat(in.ServerDir); err == nil {
						out = append(out, named{fi, in.Name})
					}
				}
			}
			return out, nil
		}
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	r, err := v.root(name)
	if err != nil {
		return nil, err
	}
	switch req.Method {
	case "Stat":
		fi, err := r.Stat(rel)
		if err != nil {
			return nil, err
		}
		if rel == "." {
			fi = named{fi, name}
		}
		return listerAt{fi}, nil
	case "Lstat":
		fi, err := r.Lstat(rel)
		if err != nil {
			return nil, err
		}
		if rel == "." {
			fi = named{fi, name}
		}
		return listerAt{fi}, nil
	case "List":
		d, err := r.Open(rel)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		ents, err := d.Readdir(-1)
		if err != nil {
			return nil, err
		}
		return listerAt(ents), nil
	case "Readlink":
		return nil, errors.New("links aren't shown")
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}
