package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// Extraction limits, so a small archive can't fill the disk.
const (
	MaxExtractBytes   = 10 << 30
	MaxExtractEntries = 200000
)

// ArchiveBody is the body of POST .../files/archive: the items in Dir to
// pack, and the archive's file name (in Dir; .zip, .tar.gz or .tgz).
type ArchiveBody struct {
	Dir   string   `json:"dir"`
	Paths []string `json:"paths"`
	Name  string   `json:"name"`
}

// ExtractBody is the body of POST .../files/extract: the archive, and the
// folder to unpack it into (default: the archive's own folder).
type ExtractBody struct {
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

func archiveKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".zip"):
		return "zip"
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return "tgz"
	case strings.HasSuffix(n, ".tar"):
		return "tar"
	}
	return ""
}

func (s *Server) filesArchive(q *request) {
	var b ArchiveBody
	if !decode(q, &b) {
		return
	}
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	dir, err := cleanRel(b.Dir)
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	kind := archiveKind(b.Name)
	if kind == "" || kind == "tar" || strings.ContainsAny(b.Name, "/\\") {
		fail(q.w, http.StatusBadRequest, errors.New("name the archive like backup.zip or backup.tar.gz"), "")
		return
	}
	if len(b.Paths) == 0 {
		fail(q.w, http.StatusBadRequest, errors.New("choose what to archive"), "")
		return
	}
	out := path.Join(dir, b.Name)
	if _, err := root.Lstat(out); err == nil {
		fail(q.w, http.StatusConflict, fmt.Errorf("%s already exists", b.Name), "")
		return
	}
	tmp := path.Join(dir, "."+b.Name+".anyserve-tmp")
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		fileErr(q.w, err)
		return
	}
	err = writeArchive(root, f, kind, dir, b.Paths, map[string]bool{tmp: true, out: true})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, out)
	}
	if err != nil {
		root.Remove(tmp)
		fail(q.w, http.StatusBadRequest, fmt.Errorf("archive: %w", err), "")
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

// writeArchive packs items (names in dir) with entry names relative to
// dir. Symbolic links and special files are left out.
func writeArchive(root *os.Root, w io.Writer, kind, dir string, items []string, skip map[string]bool) error {
	fsys := root.FS()
	var zw *zip.Writer
	var tw *tar.Writer
	var gz *gzip.Writer
	if kind == "zip" {
		zw = zip.NewWriter(w)
	} else {
		gz = gzip.NewWriter(w)
		tw = tar.NewWriter(gz)
	}
	for _, it := range items {
		// Items are plain names in dir.
		if it == "" || it == "." || it == ".." || strings.ContainsAny(it, "/\\\x00") {
			return fmt.Errorf("bad item %q", it)
		}
		rel := path.Join(dir, it)
		err := fs.WalkDir(fsys, rel, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if skip[p] || d.Type()&(fs.ModeSymlink|fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeCharDevice) != 0 {
				return nil
			}
			name := p
			if dir != "." {
				name = strings.TrimPrefix(p, dir+"/")
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				if zw != nil {
					_, err = zw.CreateHeader(&zip.FileHeader{Name: name + "/", Modified: info.ModTime()})
					return err
				}
				return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: info.ModTime()})
			}
			src, err := fsys.Open(p)
			if err != nil {
				return err
			}
			defer src.Close()
			var dst io.Writer
			if zw != nil {
				h, _ := zip.FileInfoHeader(info)
				h.Name, h.Method = name, zip.Deflate
				if dst, err = zw.CreateHeader(h); err != nil {
					return err
				}
			} else {
				if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}); err != nil {
					return err
				}
				dst = tw
			}
			_, err = io.Copy(dst, src)
			return err
		})
		if err != nil {
			return err
		}
	}
	if zw != nil {
		return zw.Close()
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (s *Server) filesExtract(q *request) {
	var b ExtractBody
	if !decode(q, &b) {
		return
	}
	root, ok := s.root(q)
	if !ok {
		return
	}
	defer root.Close()
	src, err := cleanRel(b.Path)
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	to := path.Dir(src)
	if b.To != "" {
		if to, err = cleanRel(b.To); err != nil {
			fail(q.w, http.StatusBadRequest, err, "")
			return
		}
	}
	kind := archiveKind(src)
	if kind == "" {
		fail(q.w, http.StatusBadRequest, errors.New("only .zip, .tar, .tar.gz and .tgz files can be unpacked"), "")
		return
	}
	if err := root.MkdirAll(to, 0o755); err != nil {
		fileErr(q.w, err)
		return
	}
	n, err := extract(root, src, to, kind)
	if err != nil {
		fail(q.w, http.StatusBadRequest, fmt.Errorf("unpack: %w (%d files written before it stopped)", err, n), "")
		return
	}
	writeJSON(q.w, http.StatusOK, map[string]any{"ok": true, "files": n})
}

// lim tracks extraction limits across entries.
type lim struct{ bytes, entries int64 }

func (l *lim) entry() error {
	l.entries++
	if l.entries > MaxExtractEntries {
		return fmt.Errorf("more than %d entries", MaxExtractEntries)
	}
	return nil
}

// extract unpacks an archive inside root. Every entry name is cleaned and
// placed under to; os.Root refuses anything that would land outside the
// instance's folder. Links and special entries are skipped.
func extract(root *os.Root, src, to, kind string) (int, error) {
	written := 0
	l := &lim{}
	place := func(name string) (string, bool) {
		rel, err := cleanRel(name)
		if err != nil || rel == "." {
			return "", false
		}
		return path.Join(to, rel), true
	}
	writeFile := func(dst string, mode fs.FileMode, r io.Reader) error {
		if err := root.MkdirAll(path.Dir(dst), 0o755); err != nil {
			return err
		}
		if info, err := root.Lstat(dst); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: a folder or link is in the way", dst)
		}
		f, err := root.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o600)
		if err != nil {
			return err
		}
		left := MaxExtractBytes - l.bytes
		n, err := io.Copy(f, io.LimitReader(r, left+1))
		l.bytes += n
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && l.bytes > MaxExtractBytes {
			err = fmt.Errorf("more than %d GiB unpacked", MaxExtractBytes>>30)
		}
		if err == nil {
			written++
		}
		return err
	}
	if kind == "zip" {
		f, err := root.Open(src)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return 0, err
		}
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return 0, err
		}
		for _, e := range zr.File {
			if err := l.entry(); err != nil {
				return written, err
			}
			dst, ok := place(e.Name)
			if !ok {
				continue
			}
			m := e.Mode()
			switch {
			case m.IsDir():
				if err := root.MkdirAll(dst, 0o755); err != nil {
					return written, err
				}
			case m.IsRegular():
				r, err := e.Open()
				if err != nil {
					return written, err
				}
				err = writeFile(dst, m, r)
				r.Close()
				if err != nil {
					return written, err
				}
			}
		}
		return written, nil
	}
	f, err := root.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var r io.Reader = f
	if kind == "tgz" {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
		if err := l.entry(); err != nil {
			return written, err
		}
		dst, ok := place(h.Name)
		if !ok {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(dst, 0o755); err != nil {
				return written, err
			}
		case tar.TypeReg:
			if err := writeFile(dst, fs.FileMode(h.Mode), tr); err != nil {
				return written, err
			}
		}
	}
}
