package fetch

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func TestExtractSubdirAndModes(t *testing.T) {
	z := makeZip(t, map[string]string{
		"1458/Linux/server.bin": "linux",
		"1458/Linux/lib/x.so":   "lib",
		"1458/Windows/srv.exe":  "windows",
	})
	dest := t.TempDir()
	n, err := Extract(z, "1458/Linux", dest)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "server.bin")); string(b) != "linux" {
		t.Errorf("server.bin = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, "srv.exe")); err == nil {
		t.Error("other OS files should not be extracted")
	}
	st, _ := os.Stat(filepath.Join(dest, "server.bin"))
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("executable bit lost: %v", st.Mode())
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	for _, name := range []string{"../evil", "a/../../evil", `..\evil`} {
		z := makeZip(t, map[string]string{name: "x", "ok": "y"})
		dest := filepath.Join(t.TempDir(), "d")
		os.MkdirAll(dest, 0o755)
		_, err := Extract(z, "", dest)
		// Either refused, or cleaned so it stays inside dest.
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil")); statErr == nil {
			t.Fatalf("%q escaped the destination (err=%v)", name, err)
		}
	}
}

func TestExtractTarGz(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "steamcmd.sh", Mode: 0o755, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	tw.Close()
	gz.Close()
	f.Close()
	dest := t.TempDir()
	if _, err := Extract(p, "", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); err == nil {
		t.Error("symlinks must not be extracted")
	}
}

func TestDownloadRefusesPlainHTTP(t *testing.T) {
	_, err := Download(context.Background(), "http://example.com/x.zip", "", t.TempDir(), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https error, got %v", err)
	}
}

func TestDownloadUsesVerifiedCache(t *testing.T) {
	dir := t.TempDir()
	body := []byte("cached")
	h := sha256Hex(body)
	os.WriteFile(filepath.Join(dir, h+"-x.zip"), body, 0o644)
	p, err := Download(context.Background(), "https://invalid.example/x.zip", h, dir, func(string, ...any) {})
	if err != nil {
		t.Fatalf("cached file should be used without a network request: %v", err)
	}
	if !strings.HasSuffix(p, h+"-x.zip") {
		t.Errorf("path = %s", p)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestExtractAsFormat(t *testing.T) {
	// A zip saved under a name with no extension, as some download URLs give.
	z := makeZip(t, map[string]string{"server/run.sh": "hi"})
	plain := filepath.Join(t.TempDir(), "linux64")
	b, err := os.ReadFile(z)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(plain, "", t.TempDir()); err == nil {
		t.Error("Extract guessed a type for a file with no extension")
	}
	dest := t.TempDir()
	if n, err := ExtractAs(plain, "zip", "server", dest, nil); err != nil || n != 1 {
		t.Fatalf("ExtractAs: %d files, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "run.sh")); err != nil {
		t.Error(err)
	}
}

func TestExtractKeep(t *testing.T) {
	z := makeZip(t, map[string]string{"server.properties": "shipped", "bin": "new"})
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "server.properties"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "bin"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractAs(z, "", "", dest, []string{"server.properties"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "server.properties")); string(b) != "mine" {
		t.Errorf("kept file was replaced: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "bin")); string(b) != "new" {
		t.Errorf("other file wasn't updated: %q", b)
	}
	fresh := t.TempDir()
	if _, err := ExtractAs(z, "", "", fresh, []string{"server.properties"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(fresh, "server.properties")); string(b) != "shipped" {
		t.Errorf("a missing kept file should come from the archive: %q", b)
	}
}
