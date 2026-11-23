package remote

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
)

func testHome(t *testing.T) func(io.Writer) (*app.App, error) {
	home := t.TempDir()
	dirs := paths.Dirs{Config: filepath.Join(home, "config"), Data: filepath.Join(home, "data"), Cache: filepath.Join(home, "cache")}
	return func(out io.Writer) (*app.App, error) {
		a := &app.App{Dirs: dirs, Store: instance.Store{Root: dirs.Instances()}, Out: out}
		a.Log = func(string, ...any) {}
		return a, nil
	}
}

func TestFleet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The manager.
	mApp := testHome(t)
	ma, _ := mApp(io.Discard)
	hostKey, _, err := LoadOrCreateHostKey(ma.Dirs.Config)
	if err != nil {
		t.Fatal(err)
	}
	mAPI := &api.Server{NewApp: mApp}
	srv := &Server{App: func() (*app.App, error) { return mApp(io.Discard) }, API: mAPI.Handler(), HostKey: hostKey, Log: t.Logf}
	mAPI.Fleet = srv
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ctx, l)
	addr := l.Addr().String()

	// The worker.
	wApp := testHome(t)
	wa, _ := wApp(io.Discard)
	nodeKey, err := LoadOrCreateNodeKey(wa.Dirs.Config)
	if err != nil {
		t.Fatal(err)
	}

	tok, _, err := ma.NewJoinToken(Fingerprint(hostKey.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	// A token for another manager's key is refused before anything is sent.
	secret, _, _ := app.SplitJoinToken(tok)
	if _, err := Join(addr, secret+".AAAAwrongfingerprint", "box", nodeKey); err == nil || !strings.Contains(err.Error(), "isn't the manager") {
		t.Errorf("wrong fingerprint: %v", err)
	}
	pinned, err := Join(addr, tok, "box", nodeKey)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := Join(addr, tok, "box2", nodeKey); err == nil {
		t.Error("a token worked twice")
	}

	// The worker connects and serves its API to the manager.
	link := app.ManagerLink{Addr: addr, HostKey: pinned, Name: "box"}
	wAPI := &api.Server{NewApp: wApp}
	go RunAgent(ctx, link, nodeKey, wAPI.Handler(), t.Logf)

	owner := func(method, path string) (int, string) {
		r := httptest.NewRequest(method, path, nil).WithContext(api.WithCaller(ctx, app.Owner))
		w := httptest.NewRecorder()
		mAPI.Handler().ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	deadline := time.Now().Add(5 * time.Second)
	var nodes []api.NodeInfo
	for time.Now().Before(deadline) {
		_, body := owner("GET", "/v1/nodes")
		json.Unmarshal([]byte(body), &nodes)
		if len(nodes) == 1 && nodes[0].Connected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(nodes) != 1 || !nodes[0].Connected || nodes[0].Name != "box" {
		t.Fatalf("nodes: %+v", nodes)
	}
	code, body := owner("GET", "/v1/nodes/box/v1/whoami")
	if code != http.StatusOK || !strings.Contains(body, `"name": "manager"`) {
		t.Fatalf("whoami through the node: %d %s", code, body)
	}
	if code, body := owner("GET", "/v1/nodes/box/v1/instances"); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Errorf("instances through the node: %d %s", code, body)
	}
	// Users without admin power can't reach nodes.
	alice := app.Caller{Name: "alice"}
	r := httptest.NewRequest("GET", "/v1/nodes/box/v1/whoami", nil).WithContext(api.WithCaller(ctx, alice))
	w := httptest.NewRecorder()
	mAPI.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a user reached a node: %d", w.Code)
	}
	// Removing the node cuts it off for good.
	if code, _ := owner("DELETE", "/v1/nodes/box"); code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if code, _ := owner("GET", "/v1/nodes/box/v1/whoami"); code != http.StatusBadGateway {
		t.Errorf("a removed node still answers: %d", code)
	}
}
