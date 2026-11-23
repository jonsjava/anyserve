package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/webui"
)

type fixture struct {
	addr     string
	hostKey  ssh.PublicKey
	aliceKey string // private key file
	ports    map[string]int
	home     string
	newApp   func(io.Writer) (*app.App, error)
	ca       *WebCA
}

func newKey(t *testing.T, dir, name string) (string, string) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	p := filepath.Join(dir, name)
	os.WriteFile(p, pemEncode(block), 0o600)
	sp, _ := ssh.NewPublicKey(pub)
	return p, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
}

func setup(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	dirs := paths.Dirs{Config: filepath.Join(home, "config"), Data: filepath.Join(home, "data"), Cache: filepath.Join(home, "cache")}
	newApp := func(out io.Writer) (*app.App, error) {
		a := &app.App{Dirs: dirs, Store: instance.Store{Root: dirs.Instances()}, Out: out}
		a.Log = func(string, ...any) {}
		return a, nil
	}
	d, err := spec.LoadFile(filepath.Join("..", "..", "registry", "services", "gotify.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(io.Discard)
	f := &fixture{home: home, ports: map[string]int{}}
	for _, n := range []string{"alpha", "beta"} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		f.ports[n] = port
		go func() { // an echo server standing in for the instance
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() { io.Copy(c, c); c.Close() }()
			}
		}()
		t.Cleanup(func() { l.Close() })
		in := &instance.Instance{Name: n, Service: d.ID, Registry: "main", DefinitionVersion: d.Version, Target: "docker", Settings: map[string]string{"admin_password": "x"}, Ports: map[string]int{"http": port}}
		in.ServerDir = filepath.Join(a.Store.Path(n), "server")
		if err := a.Store.Save(in, d); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(in.ServerDir, "data"), 0o755)
		os.WriteFile(filepath.Join(in.ServerDir, "data", "hello.txt"), []byte("hi from "+n), 0o644)
	}
	var pub string
	f.aliceKey, pub = newKey(t, home, "alice")
	if err := a.AddUser("alice", false, []string{pub}); err != nil {
		t.Fatal(err)
	}
	if err := a.Grant("alice", "alpha"); err != nil {
		t.Fatal(err)
	}
	f.newApp = newApp
	if f.ca, err = LoadOrCreateWebCA(dirs.Config, "test"); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	return f
}

func (f *fixture) start(t *testing.T) {
	signer, _, err := LoadOrCreateHostKey(filepath.Join(f.home, "config"))
	if err != nil {
		t.Fatal(err)
	}
	f.hostKey = signer.PublicKey()
	srv := &Server{
		App:     func() (*app.App, error) { return f.newApp(io.Discard) },
		API:     (&api.Server{NewApp: f.newApp}).Handler(),
		HostKey: signer,
		WebCA:   f.ca,
		Log:     t.Logf,
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, l)
	f.addr = l.Addr().String()
}

func (f *fixture) host(user, key string) Host {
	return Host{Name: "test", Addr: f.addr, User: user, Key: key, HostKey: string(ssh.MarshalAuthorizedKey(f.hostKey))}
}

func TestLogin(t *testing.T) {
	f := setup(t)
	if _, err := Dial(f.host("bob", f.aliceKey)); err == nil {
		t.Error("alice's key logged in as bob")
	}
	other, _ := newKey(t, f.home, "other")
	if _, err := Dial(f.host("alice", other)); err == nil {
		t.Error("an unknown key logged in")
	}
	// A different pinned host key is refused.
	h := f.host("alice", f.aliceKey)
	_, fake := newKey(t, f.home, "fakehost")
	h.HostKey = fake
	if _, err := Dial(h); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("wrong host key: %v", err)
	}
	k, err := Probe(f.host("alice", f.aliceKey))
	if err != nil || Fingerprint(k) != Fingerprint(f.hostKey) {
		t.Errorf("probe: %v", err)
	}
}

func TestAPIOverSSH(t *testing.T) {
	f := setup(t)
	c, err := Dial(f.host("alice", f.aliceKey))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hc := APIClient(c)
	resp, err := hc.Get(APIURL + "/v1/whoami")
	if err != nil {
		t.Fatal(err)
	}
	var who api.Whoami
	json.NewDecoder(resp.Body).Decode(&who)
	resp.Body.Close()
	if who.Name != "alice" || who.Admin || who.Local {
		t.Errorf("whoami = %+v", who)
	}
	resp, _ = hc.Get(APIURL + "/v1/instances")
	var list []api.Instance
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "alpha" {
		t.Errorf("list = %+v", list)
	}
}

func TestForward(t *testing.T) {
	f := setup(t)
	c, err := Dial(f.host("alice", f.aliceKey))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.Dial("tcp", "127.0.0.1:"+strconv.Itoa(f.ports["alpha"]))
	if err != nil {
		t.Fatalf("forward to alpha: %v", err)
	}
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	io.ReadFull(conn, buf)
	conn.Close()
	if string(buf) != "ping" {
		t.Errorf("echo = %q", buf)
	}
	for _, addr := range []string{
		"127.0.0.1:" + strconv.Itoa(f.ports["beta"]), // not alice's
		"127.0.0.1:22", // not an instance
		"10.0.0.1:" + strconv.Itoa(f.ports["alpha"]), // not this host
	} {
		if conn, err := c.Dial("tcp", addr); err == nil {
			conn.Close()
			t.Errorf("forward to %s was allowed", addr)
		}
	}
}

func TestSFTP(t *testing.T) {
	f := setup(t)
	c, err := Dial(f.host("alice", f.aliceKey))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	ents, err := sc.ReadDir("/")
	if err != nil || len(ents) != 1 || ents[0].Name() != "alpha" {
		t.Fatalf("root listing: %v %v", ents, err)
	}
	fh, err := sc.Open("/alpha/data/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(fh)
	fh.Close()
	if string(b) != "hi from alpha" {
		t.Errorf("read %q", b)
	}
	w, err := sc.Create("/alpha/data/up.txt")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("uploaded"))
	w.Close()
	got, _ := os.ReadFile(filepath.Join(f.home, "data", "instances", "alpha", "server", "data", "up.txt"))
	if string(got) != "uploaded" {
		t.Errorf("upload landed as %q", got)
	}
	for _, p := range []string{"/beta/data/hello.txt", "/alpha/../beta/data/hello.txt", "/alpha/data/../../../beta/server/data/hello.txt"} {
		if fh, err := sc.Open(p); err == nil {
			b, _ := io.ReadAll(fh)
			fh.Close()
			if bytes.Contains(b, []byte("beta")) {
				t.Errorf("read beta's file via %s", p)
			}
		}
	}
	betaDir := filepath.Join(f.home, "data", "instances", "beta", "server", "data")
	os.Symlink(betaDir, filepath.Join(f.home, "data", "instances", "alpha", "server", "data", "escape"))
	if fh, err := sc.Open("/alpha/data/escape/hello.txt"); err == nil {
		fh.Close()
		t.Error("followed a symlink out of alpha")
	}
	if w, err := sc.Create("/alpha/data/escape/pwned"); err == nil {
		w.Close()
		t.Error("created a file through a symlink")
	}
	if err := sc.Symlink("/etc/passwd", "/alpha/data/pw"); err == nil {
		t.Error("created a symlink")
	}
	if err := sc.Rename("/alpha/data/up.txt", "/beta/data/up.txt"); err == nil {
		t.Error("moved a file into beta")
	}
}

func TestNoShell(t *testing.T) {
	f := setup(t)
	c, err := Dial(f.host("alice", f.aliceKey))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	s.Stderr = &stderr
	err = s.Run("id")
	if err == nil || !strings.Contains(stderr.String(), "no shell") {
		t.Errorf("exec: %v, stderr %q", err, stderr.String())
	}
}

func TestWebClientCert(t *testing.T) {
	f := setup(t)
	a, _ := f.newApp(io.Discard)
	samKey, samPub := newKey(t, f.home, "sam")
	if err := a.AddUser("sam", true, []string{samPub}); err != nil {
		t.Fatal(err)
	}
	cc, err := f.ca.IssueClientCert("sam", time.Hour, "pw")
	if err != nil {
		t.Fatal(err)
	}
	a.UpdateUser("sam", func(u *app.User) error { u.CertSerials = append(u.CertSerials, cc.Serial); return nil })
	key, cert, _, err := pkcs12.DecodeChain(cc.P12, "pw")
	if err != nil {
		t.Fatal(err)
	}
	clientCert := tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key}

	c, err := Dial(f.host("sam", samKey))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := APIClient(c).Post(APIURL+"/v1/web-session", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var sess WebSession
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if sess.Token == "" || !strings.Contains(sess.CA, "BEGIN CERTIFICATE") {
		t.Fatalf("session = %+v", sess)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(sess.CA))

	web := func(withCert bool, token string) (int, int) {
		cfg := &tls.Config{RootCAs: pool, ServerName: "localhost"}
		if withCert {
			cfg.Certificates = []tls.Certificate{clientCert}
		}
		hc := &http.Client{Transport: &http.Transport{
			TLSClientConfig: cfg,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.DialContext(ctx, "tcp", "web.anyserve:443")
			},
		}}
		req, _ := http.NewRequest("GET", "https://127.0.0.1:7080/api/v1/instances", nil)
		req.Host = "127.0.0.1:7080"
		req.Header.Set(webui.TokenHeader, token)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("web request: %v", err)
		}
		defer resp.Body.Close()
		var list []api.Instance
		json.NewDecoder(resp.Body).Decode(&list)
		return resp.StatusCode, len(list)
	}
	if code, n := web(false, sess.Token); code != 200 || n != 0 {
		t.Errorf("admin without a client certificate: %d, %d instances", code, n)
	}
	if code, n := web(true, sess.Token); code != 200 || n != 2 {
		t.Errorf("admin with a client certificate: %d, %d instances", code, n)
	}
	if code, _ := web(true, "wrong"); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	// alice's token doesn't work on sam's connection.
	ac, _ := Dial(f.host("alice", f.aliceKey))
	defer ac.Close()
	resp, _ = APIClient(ac).Post(APIURL+"/v1/web-session", "", nil)
	var asess WebSession
	json.NewDecoder(resp.Body).Decode(&asess)
	resp.Body.Close()
	if code, _ := web(true, asess.Token); code != 401 {
		t.Errorf("another user's token: %d", code)
	}
	// Revoked: the certificate no longer counts.
	a.UpdateUser("sam", func(u *app.User) error { u.CertSerials = nil; return nil })
	if code, n := web(true, sess.Token); code != 200 || n != 0 {
		t.Errorf("revoked certificate: %d, %d instances", code, n)
	}
	// A certificate from another CA fails the TLS handshake's check.
	other, _ := LoadOrCreateWebCA(t.TempDir(), "other")
	occ, _ := other.IssueClientCert("sam", time.Hour, "pw")
	a.UpdateUser("sam", func(u *app.User) error { u.CertSerials = []string{occ.Serial}; return nil })
	okey, ocert, _, _ := pkcs12.DecodeChain(occ.P12, "pw")
	clientCert = tls.Certificate{Certificate: [][]byte{ocert.Raw}, PrivateKey: okey}
	func() {
		defer func() { recover() }()
		if code, n := web(true, sess.Token); code == 200 && n != 0 {
			t.Errorf("foreign certificate gave %d instances", n)
		}
	}()
}

func TestWebCAConstrained(t *testing.T) {
	ca, err := LoadOrCreateWebCA(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ca.Cert.PermittedDNSDomains) != 1 || ca.Cert.PermittedDNSDomains[0] != "localhost" || !ca.Cert.PermittedDNSDomainsCritical {
		t.Errorf("CA isn't constrained to localhost: %v", ca.Cert.PermittedDNSDomains)
	}
}
