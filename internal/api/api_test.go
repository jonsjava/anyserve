package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/spec"
)

// testServer sets up two instances (alpha, beta) and a user, alice, who
// may manage only alpha.
func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	dirs := paths.Dirs{Config: filepath.Join(home, "config"), Data: filepath.Join(home, "data"), Cache: filepath.Join(home, "cache")}
	newApp := func(out io.Writer) (*app.App, error) {
		a := &app.App{Dirs: dirs, Store: instance.Store{Root: dirs.Instances()}, Out: out}
		a.Log = func(f string, args ...any) {}
		return a, nil
	}
	d, err := spec.LoadFile(filepath.Join("..", "..", "registry", "services", "gotify.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(io.Discard)
	for _, n := range []string{"alpha", "beta"} {
		in := &instance.Instance{Name: n, Service: d.ID, Registry: "main", DefinitionVersion: d.Version, Target: "docker", Settings: map[string]string{"admin_password": "x"}, Ports: map[string]int{}}
		in.ServerDir = filepath.Join(a.Store.Path(n), "server")
		if err := a.Store.Save(in, d); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(in.ServerDir, "data"), 0o755)
		os.WriteFile(filepath.Join(in.ServerDir, "data", "hello.txt"), []byte("hi from "+n), 0o644)
	}
	if err := a.AddUser("alice", false, []string{"ssh-ed25519 AAAA test"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Grant("alice", "alpha"); err != nil {
		t.Fatal(err)
	}
	return &Server{NewApp: newApp}, home
}

func call(t *testing.T, s *Server, c *app.Caller, method, url string, body io.Reader) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, url, body)
	if c != nil {
		r = r.WithContext(WithCaller(context.Background(), *c))
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestAccess(t *testing.T) {
	s, _ := testServer(t)
	alice := app.Caller{Name: "alice"}
	mallory := app.Caller{Name: "mallory"}

	if code, _ := call(t, s, nil, "GET", "/v1/instances", nil); code != http.StatusUnauthorized {
		t.Errorf("no caller: %d", code)
	}
	_, body := call(t, s, &app.Owner, "GET", "/v1/instances", nil)
	var list []Instance
	json.Unmarshal([]byte(body), &list)
	if len(list) != 2 {
		t.Errorf("owner sees %d instances: %s", len(list), body)
	}
	_, body = call(t, s, &alice, "GET", "/v1/instances", nil)
	list = nil
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0].Name != "alpha" {
		t.Errorf("alice sees %s", body)
	}
	if code, _ := call(t, s, &alice, "GET", "/v1/instances/beta", nil); code != http.StatusNotFound {
		t.Errorf("alice reading beta: %d", code)
	}
	if code, _ := call(t, s, &alice, "GET", "/v1/instances/beta/files?path=data/hello.txt", nil); code != http.StatusNotFound {
		t.Errorf("alice reading beta's files: %d", code)
	}
	if code, _ := call(t, s, &mallory, "GET", "/v1/instances/alpha", nil); code != http.StatusNotFound {
		t.Errorf("unknown user reading alpha: %d", code)
	}
	code, body := call(t, s, &alice, "GET", "/v1/instances/alpha/files?path=data/hello.txt", nil)
	if code != 200 || body != "hi from alpha" {
		t.Errorf("alice reading her file: %d %q", code, body)
	}
}

func TestFiles(t *testing.T) {
	s, home := testServer(t)
	alice := app.Caller{Name: "alice"}
	base := "/v1/instances/alpha/files"

	if code, body := call(t, s, &alice, "PUT", base+"?path=data/new/../up.txt", strings.NewReader("uploaded")); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	_, body := call(t, s, &alice, "GET", base+"?path=data/up.txt", nil)
	if body != "uploaded" {
		t.Errorf("read back %q", body)
	}
	_, body = call(t, s, &alice, "GET", base+"?path=/data", nil)
	var ents []FileEntry
	json.Unmarshal([]byte(body), &ents)
	if len(ents) != 2 {
		t.Errorf("listing: %s", body)
	}
	if code, _ := call(t, s, &alice, "POST", base+"/mkdir?path=data/sub/deeper", nil); code != 200 {
		t.Errorf("mkdir: %d", code)
	}
	if code, _ := call(t, s, &alice, "POST", base+"/rename?from=data/up.txt&to=data/sub/up.txt", nil); code != 200 {
		t.Errorf("rename: %d", code)
	}
	if code, _ := call(t, s, &alice, "DELETE", base+"?path=data/sub", nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := call(t, s, &alice, "DELETE", base+"?path=/", nil); code != http.StatusBadRequest {
		t.Errorf("deleting the root: %d", code)
	}

	// ".." can't climb out: it's cleaned against the instance's root.
	_, body = call(t, s, &alice, "GET", base+"?path=../../beta/server/data/hello.txt", nil)
	if strings.Contains(body, "beta") {
		t.Errorf("climbed into beta: %q", body)
	}
	// A symlink planted by the server can't be followed out either.
	serverDir := filepath.Join(home, "data", "instances", "alpha", "server")
	betaFile := filepath.Join(home, "data", "instances", "beta", "server", "data", "hello.txt")
	os.Symlink(betaFile, filepath.Join(serverDir, "data", "escape.txt"))
	os.Symlink(filepath.Dir(betaFile), filepath.Join(serverDir, "data", "escapedir"))
	for _, p := range []string{"data/escape.txt", "data/escapedir/hello.txt", "data/escapedir"} {
		code, body := call(t, s, &alice, "GET", base+"?path="+p, nil)
		if code == 200 || strings.Contains(body, "hi from beta") {
			t.Errorf("followed a symlink out via %s: %d %q", p, code, body)
		}
	}
	if code, _ := call(t, s, &alice, "PUT", base+"?path=data/escapedir/pwned.txt", bytes.NewBufferString("x")); code == 200 {
		t.Error("wrote through a symlink out of the root")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(betaFile), "pwned.txt")); err == nil {
		t.Error("pwned.txt exists in beta")
	}
	// Deleting the link removes the link, not what it points to.
	call(t, s, &alice, "DELETE", base+"?path=data/escapedir", nil)
	if _, err := os.Stat(betaFile); err != nil {
		t.Errorf("deleting a link removed its target: %v", err)
	}
}

func TestAudit(t *testing.T) {
	s, _ := testServer(t)
	var log bytes.Buffer
	s.Audit = &log
	alice := app.Caller{Name: "alice"}
	call(t, s, &alice, "GET", "/v1/instances", nil)
	call(t, s, &alice, "POST", "/v1/instances/alpha/files/mkdir?path=x", nil)
	call(t, s, &alice, "POST", "/v1/instances/beta/files/mkdir?path=x", nil)
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "alice POST /v1/instances/alpha/files/mkdir?path=x 200") || !strings.Contains(lines[1], " 404 ") {
		t.Errorf("audit log:\n%s", log.String())
	}
}
