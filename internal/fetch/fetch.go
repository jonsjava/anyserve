// Package fetch downloads server files: plain archives over HTTPS, and
// Steam apps through SteamCMD.
package fetch

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// Progress receives human-readable status lines.
type Progress func(format string, a ...any)

// Download fetches url into the cache directory and returns the file path.
// If want (a sha256 hex string) is set, the file must match it, and a cached
// file that already matches is reused without downloading.
func Download(ctx context.Context, url, want, cacheDir string, log Progress) (string, error) {
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("refusing to download %s: only https:// is allowed", url)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	name := path.Base(url)
	if want != "" {
		cached := filepath.Join(cacheDir, want+"-"+name)
		if sum, err := fileSHA256(cached); err == nil && sum == want {
			log("Using cached download %s", name)
			return cached, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "anyserve")
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	// Without a checksum, an HTML error page is the likeliest wrong file.
	// With one, the checksum already proves it's the right file.
	if ct := resp.Header.Get("Content-Type"); want == "" && strings.HasPrefix(ct, "text/") {
		return "", fmt.Errorf("download %s: server returned %s instead of a file (has the version been removed?)", url, ct)
	}

	tmp, err := os.CreateTemp(cacheDir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	log("Downloading %s", url)
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, log: log, name: name}
	if _, err := io.Copy(io.MultiWriter(tmp, h), pr); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		return "", fmt.Errorf("download %s: checksum mismatch (want %s, got %s); the file may have been changed or tampered with", url, want, got)
	}
	final := filepath.Join(cacheDir, got+"-"+name)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
}

type progressReader struct {
	r       io.Reader
	total   int64
	done    int64
	lastPct int64
	log     Progress
	name    string
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.total > 0 {
		pct := p.done * 100 / p.total
		if pct >= p.lastPct+25 {
			p.lastPct = pct - pct%25
			p.log("  %s: %d%% of %d MB", p.name, p.lastPct, p.total>>20)
		}
	}
	return n, err
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Extract unpacks the part of an archive under subdir into dest. Paths in
// the archive that would land outside dest are rejected. Existing files are
// replaced, and files not in the archive are left alone, so saves survive.
func Extract(archive, subdir, dest string) (int, error) {
	subdir = strings.Trim(path.Clean("/"+subdir), "/")
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, subdir, dest)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTar(archive, subdir, dest, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
	case strings.HasSuffix(archive, ".tar.xz"), strings.HasSuffix(archive, ".txz"):
		return extractTar(archive, subdir, dest, func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) })
	}
	return 0, fmt.Errorf("%s: unsupported archive type", archive)
}

// target maps an archive entry name to a destination path, or "" to skip.
func target(name, subdir, dest string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	if subdir != "" {
		if clean != subdir && !strings.HasPrefix(clean, subdir+"/") {
			return "", nil
		}
		clean = strings.TrimPrefix(strings.TrimPrefix(clean, subdir), "/")
	}
	if clean == "" || clean == "." {
		return "", nil
	}
	out := filepath.Join(dest, filepath.FromSlash(clean))
	rel, err := filepath.Rel(dest, out)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the install directory", name)
	}
	return out, nil
}

func extractZip(archive, subdir, dest string) (int, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	n := 0
	for _, f := range zr.File {
		out, err := target(f.Name, subdir, dest)
		if err != nil {
			return n, err
		}
		if out == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return n, err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			// Symlinks could point anywhere; skip them.
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		err = writeFile(out, rc, f.Mode().Perm())
		rc.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("%s: nothing found under %q", filepath.Base(archive), subdir)
	}
	return n, nil
}

func extractTar(archive, subdir, dest string, decompress func(io.Reader) (io.Reader, error)) (int, error) {
	f, err := os.Open(archive)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := decompress(bufio.NewReader(f))
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(zr)
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n, err
		}
		out, err := target(h.Name, subdir, dest)
		if err != nil {
			return n, err
		}
		if out == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := writeFile(out, tr, os.FileMode(h.Mode).Perm()); err != nil {
				return n, err
			}
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("%s: nothing found under %q", filepath.Base(archive), subdir)
	}
	return n, nil
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	mode |= 0o600
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".anyserve-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}
