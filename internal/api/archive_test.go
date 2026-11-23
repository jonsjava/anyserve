package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/app"
)

func TestArchiveRoundTrip(t *testing.T) {
	s, home := testServer(t)
	alice := app.Caller{Name: "alice"}
	server := filepath.Join(home, "data", "instances", "alpha", "server")
	os.MkdirAll(filepath.Join(server, "data", "sub"), 0o755)
	os.WriteFile(filepath.Join(server, "data", "sub", "deep.txt"), []byte("deep"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(server, "data", "link"))
	for _, name := range []string{"w.zip", "w.tar.gz"} {
		code, body := call(t, s, &alice, "POST", "/v1/instances/alpha/files/archive", strings.NewReader(`{"dir":"","paths":["data"],"name":"`+name+`"}`))
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		to := "out-" + strings.ReplaceAll(name, ".", "-")
		code, body = call(t, s, &alice, "POST", "/v1/instances/alpha/files/extract", strings.NewReader(`{"path":"`+name+`","to":"`+to+`"}`))
		if code != http.StatusOK {
			t.Fatalf("extract %s: %d %s", name, code, body)
		}
		got, err := os.ReadFile(filepath.Join(server, to, "data", "sub", "deep.txt"))
		if err != nil || string(got) != "deep" {
			t.Errorf("%s round trip: %q %v", name, got, err)
		}
		if _, err := os.Lstat(filepath.Join(server, to, "data", "link")); err == nil {
			t.Errorf("%s packed a symlink", name)
		}
	}
	if code, _ := call(t, s, &alice, "POST", "/v1/instances/alpha/files/archive", strings.NewReader(`{"paths":["../beta"],"name":"x.zip"}`)); code != http.StatusBadRequest {
		t.Errorf("archived outside the folder: %d", code)
	}
	if code, _ := call(t, s, &alice, "POST", "/v1/instances/beta/files/archive", strings.NewReader(`{"paths":["data"],"name":"x.zip"}`)); code != http.StatusNotFound {
		t.Errorf("archived someone else's instance: %d", code)
	}
}

func TestExtractHostile(t *testing.T) {
	s, home := testServer(t)
	alice := app.Caller{Name: "alice"}
	server := filepath.Join(home, "data", "instances", "alpha", "server")
	// A zip with a path-traversal name and an absolute name.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, n := range []string{"../../escape.txt", "/abs.txt"} {
		w, _ := zw.Create(n)
		w.Write([]byte("x"))
	}
	zw.Close()
	os.WriteFile(filepath.Join(server, "evil.zip"), zb.Bytes(), 0o644)
	if code, body := call(t, s, &alice, "POST", "/v1/instances/alpha/files/extract", strings.NewReader(`{"path":"evil.zip","to":"in"}`)); code != http.StatusOK {
		t.Fatalf("extract: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "instances", "escape.txt")); err == nil {
		t.Error("zip slip escaped the folder")
	}
	if _, err := os.Stat(filepath.Join(server, "in", "escape.txt")); err != nil {
		t.Errorf("the traversal entry should land inside: %v", err)
	}
	// A tar with a symlink, then a file written through it.
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/tmp"})
	tw.WriteHeader(&tar.Header{Name: "l/owned.txt", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	os.WriteFile(filepath.Join(server, "evil.tgz"), tb.Bytes(), 0o644)
	call(t, s, &alice, "POST", "/v1/instances/alpha/files/extract", strings.NewReader(`{"path":"evil.tgz","to":"in2"}`))
	if info, err := os.Lstat(filepath.Join(server, "in2", "l")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("a symlink was unpacked")
	}
	if _, err := os.Stat("/tmp/owned.txt"); err == nil {
		t.Error("wrote through a symlink")
	}
}
