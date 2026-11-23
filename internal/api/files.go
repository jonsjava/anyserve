package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"
)

// MaxUpload is the largest file the API accepts in one PUT.
const MaxUpload = 2 << 30

// FileEntry is one item in a folder listing.
type FileEntry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Mode     string    `json:"mode"`
	Modified time.Time `json:"modified"`
	// Link is set for symbolic links, which are shown but not followed
	// out of the instance's folder.
	Link bool `json:"link,omitempty"`
}

// root opens the instance's server folder as an os.Root: every file
// operation below goes through it, so no path, symlink or ".." can reach
// outside the folder.
func (s *Server) root(q *request) (*os.Root, bool) {
	in, _, err := q.app.Store.Load(q.name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return nil, false
	}
	r, err := os.OpenRoot(in.ServerDir)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return nil, false
	}
	return r, true
}

func (s *Server) relPath(q *request, key string) (string, bool) {
	rel, err := cleanRel(q.r.URL.Query().Get(key))
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return "", false
	}
	return rel, true
}

func fileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fail(w, http.StatusNotFound, errors.New("no such file or folder"), "")
	case errors.Is(err, fs.ErrPermission):
		fail(w, http.StatusForbidden, errors.New("permission denied"), "")
	case errors.Is(err, fs.ErrExist):
		fail(w, http.StatusConflict, errors.New("already exists"), "")
	default:
		// Escapes from the root land here ("path escapes from parent").
		fail(w, http.StatusBadRequest, err, "")
	}
}

// filesGet lists a folder, or downloads a file.
func (s *Server) filesGet(q *request) {
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := s.relPath(q, "path")
	if !ok {
		return
	}
	info, err := root.Stat(rel)
	if err != nil {
		fileErr(q.w, err)
		return
	}
	if info.IsDir() {
		d, err := root.Open(rel)
		if err != nil {
			fileErr(q.w, err)
			return
		}
		defer d.Close()
		ents, err := d.ReadDir(-1)
		if err != nil {
			fileErr(q.w, err)
			return
		}
		out := []FileEntry{}
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, FileEntry{Name: e.Name(), Dir: e.IsDir(), Size: fi.Size(), Mode: fi.Mode().String(), Modified: fi.ModTime().UTC(), Link: fi.Mode()&fs.ModeSymlink != 0})
		}
		sortEntries(out)
		writeJSON(q.w, http.StatusOK, out)
		return
	}
	if !info.Mode().IsRegular() {
		fail(q.w, http.StatusBadRequest, errors.New("not a regular file"), "")
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		fileErr(q.w, err)
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(path.Ext(rel))
	if ct == "" {
		ct = "application/octet-stream"
	}
	q.w.Header().Set("Content-Type", ct)
	q.w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	q.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(rel)))
	// Never let a browser render uploaded files as pages.
	q.w.Header().Set("X-Content-Type-Options", "nosniff")
	q.w.Header().Set("Content-Security-Policy", "sandbox")
	io.Copy(q.w, f)
}

// filesPut writes a file, replacing it if it exists. The body is written
// to a temporary file first, so a broken upload doesn't leave half a file.
func (s *Server) filesPut(q *request) {
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := s.relPath(q, "path")
	if !ok {
		return
	}
	if rel == "." {
		fail(q.w, http.StatusBadRequest, errors.New("give a file path"), "")
		return
	}
	if info, err := root.Lstat(rel); err == nil && !info.Mode().IsRegular() {
		fail(q.w, http.StatusConflict, errors.New("a folder or link is in the way"), "")
		return
	}
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+".anyserve-upload")
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		fileErr(q.w, err)
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(q.w, q.r.Body, MaxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		root.Remove(tmp)
		fail(q.w, http.StatusBadRequest, fmt.Errorf("upload: %w", err), "")
		return
	}
	if err := root.Rename(tmp, rel); err != nil {
		root.Remove(tmp)
		fileErr(q.w, err)
		return
	}
	writeJSON(q.w, http.StatusOK, map[string]any{"ok": true, "size": n})
}

// filesDelete removes a file, or a folder with everything in it.
func (s *Server) filesDelete(q *request) {
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := s.relPath(q, "path")
	if !ok {
		return
	}
	if rel == "." {
		fail(q.w, http.StatusBadRequest, errors.New("won't delete the instance's whole folder"), "")
		return
	}
	if _, err := root.Lstat(rel); err != nil {
		fileErr(q.w, err)
		return
	}
	if err := root.RemoveAll(rel); err != nil {
		fileErr(q.w, err)
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

func (s *Server) filesMkdir(q *request) {
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	rel, ok := s.relPath(q, "path")
	if !ok {
		return
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		fileErr(q.w, err)
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

func (s *Server) filesRename(q *request) {
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	from, ok := s.relPath(q, "from")
	if !ok {
		return
	}
	to, ok := s.relPath(q, "to")
	if !ok {
		return
	}
	if from == "." || to == "." {
		fail(q.w, http.StatusBadRequest, errors.New("give both paths"), "")
		return
	}
	if _, err := root.Lstat(to); err == nil {
		fail(q.w, http.StatusConflict, errors.New("something already has that name"), "")
		return
	}
	if err := root.Rename(from, to); err != nil {
		fileErr(q.w, err)
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}
