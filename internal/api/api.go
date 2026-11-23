// Package api serves Anyserve's HTTP API: what the web UI and remote
// clients use to see and manage instances.
//
// The API never listens on the network by itself. Locally it's served on a
// unix socket readable only by the host's user, who is the owner (an
// admin). Remote users reach it through the built-in SSH server, which
// tells it who they are; every request is checked against their grants.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/target"
)

type callerKey struct{}

// WithCaller attaches the caller to a context; the SSH server uses it for
// each connection.
func WithCaller(ctx context.Context, c app.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the request's caller. Requests without one are
// refused.
func CallerFrom(ctx context.Context) (app.Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(app.Caller)
	return c, ok
}

// Server handles API requests.
type Server struct {
	// NewApp makes an App whose output goes to out; each request gets its
	// own so its log can be returned.
	NewApp func(out io.Writer) (*app.App, error)
	// Audit receives one line per change (nil disables auditing).
	Audit io.Writer

	locks   sync.Map // instance name → *sync.Mutex
	auditMu sync.Mutex
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/whoami", s.whoami)
	m.HandleFunc("GET /v1/instances", s.list)
	m.HandleFunc("GET /v1/instances/{name}", s.instance(s.status))
	m.HandleFunc("GET /v1/instances/{name}/logs", s.instance(s.logs))
	m.HandleFunc("GET /v1/instances/{name}/backups", s.instance(s.backups))
	m.HandleFunc("GET /v1/instances/{name}/ports", s.instance(s.ports))
	for _, act := range []string{"start", "stop", "restart", "update", "backup"} {
		m.HandleFunc("POST /v1/instances/{name}/"+act, s.instance(s.locked(s.action(act))))
	}
	m.HandleFunc("POST /v1/instances/{name}/console", s.instance(s.console))
	m.HandleFunc("GET /v1/instances/{name}/files", s.instance(s.filesGet))
	m.HandleFunc("PUT /v1/instances/{name}/files", s.instance(s.filesPut))
	m.HandleFunc("DELETE /v1/instances/{name}/files", s.instance(s.filesDelete))
	m.HandleFunc("POST /v1/instances/{name}/files/mkdir", s.instance(s.filesMkdir))
	m.HandleFunc("POST /v1/instances/{name}/files/rename", s.instance(s.filesRename))
	return s.audited(m)
}

// Error is the body of a failed request.
type Error struct {
	Error string `json:"error"`
	Log   string `json:"log,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, err error, log string) {
	writeJSON(w, code, Error{Error: err.Error(), Log: log})
}

type request struct {
	w      http.ResponseWriter
	r      *http.Request
	caller app.Caller
	app    *app.App
	out    *bytes.Buffer
	name   string
}

// instance resolves the caller and instance, and refuses callers who may
// not manage it. Unknown and forbidden instances look the same, so users
// can't probe for names.
func (s *Server) instance(h func(q *request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := s.begin(w, r)
		if !ok {
			return
		}
		q.name = r.PathValue("name")
		if instance.ValidateName(q.name) != nil || !q.app.Store.Exists(q.name) || !q.app.CanManage(q.caller, q.name) {
			fail(w, http.StatusNotFound, fmt.Errorf("no instance %q", q.name), "")
			return
		}
		h(q)
	}
}

func (s *Server) begin(w http.ResponseWriter, r *http.Request) (*request, bool) {
	c, ok := CallerFrom(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, errors.New("no caller identity"), "")
		return nil, false
	}
	out := &bytes.Buffer{}
	a, err := s.NewApp(&syncWriter{w: out})
	if err != nil {
		fail(w, http.StatusInternalServerError, err, "")
		return nil, false
	}
	return &request{w: w, r: r, caller: c, app: a, out: out}, true
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// locked runs one change at a time per instance.
func (s *Server) locked(h func(q *request)) func(q *request) {
	return func(q *request) {
		v, _ := s.locks.LoadOrStore(q.name, &sync.Mutex{})
		mu := v.(*sync.Mutex)
		if !mu.TryLock() {
			fail(q.w, http.StatusConflict, fmt.Errorf("%s is busy with another change; try again when it finishes", q.name), "")
			return
		}
		defer mu.Unlock()
		h(q)
	}
}

// Whoami is the caller as the API sees them.
type Whoami struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	Local bool   `json:"local"`
	Host  string `json:"host"`
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	q, ok := s.begin(w, r)
	if !ok {
		return
	}
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, Whoami{Name: q.caller.Name, Admin: q.caller.Admin, Local: q.caller.Local, Host: host})
}

// Instance is one instance as the API shows it.
type Instance struct {
	Name       string    `json:"name"`
	Service    string    `json:"service"`
	ServiceID  string    `json:"service_id"`
	Registry   string    `json:"registry"`
	Definition string    `json:"definition"`
	Target     string    `json:"target"`
	State      string    `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	Upgrade    string    `json:"upgrade,omitempty"`
	AutoUpdate string    `json:"auto_update"`
	Ports      []Port    `json:"ports"`
	Console    string    `json:"console,omitempty"`
	Backups    bool      `json:"backups"`
	Installed  time.Time `json:"installed"`
	Updated    time.Time `json:"updated"`
}

// Port is one of an instance's ports.
type Port struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Public      bool   `json:"public"`
	Exposed     bool   `json:"exposed,omitempty"`
	Description string `json:"description,omitempty"`
}

func describe(a *app.App, l app.StatusLine) Instance {
	in, d := l.Instance, l.Def
	out := Instance{
		Name: in.Name, Service: d.Name, ServiceID: in.Service, Registry: in.Registry,
		Definition: in.DefinitionVersion, Target: in.Target,
		State: l.Status.State, Detail: l.Status.Detail, Upgrade: l.Upgrade,
		AutoUpdate: app.Policy(in), Backups: len(d.Backup.Paths) > 0,
		Installed: in.Installed, Updated: in.Updated,
	}
	ports, _ := d.ResolvePorts(in.Ports)
	for _, p := range d.Ports {
		out.Ports = append(out.Ports, Port{Name: p.Name, Port: ports[p.Name], Protocol: p.Protocol, Public: p.Public, Exposed: in.Exposed(p.Name), Description: p.Description})
	}
	if info, err := a.ConsoleDetails(in.Name); err == nil {
		out.Console = string(info.Kind)
	}
	return out
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q, ok := s.begin(w, r)
	if !ok {
		return
	}
	lines, _, err := q.app.Statuses()
	if err != nil {
		fail(w, http.StatusInternalServerError, err, "")
		return
	}
	out := []Instance{}
	for _, l := range lines {
		if q.app.CanManage(q.caller, l.Instance.Name) {
			out = append(out, describe(q.app, l))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) status(q *request) {
	l, err := q.app.Status(q.name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, describe(q.app, l))
}

func (s *Server) ports(q *request) {
	report, err := q.app.PortReport(q.name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, map[string]string{"report": report})
}

func (s *Server) logs(q *request) {
	lines := 200
	if n, err := strconv.Atoi(q.r.URL.Query().Get("lines")); err == nil && n > 0 {
		lines = min(n, 5000)
	}
	in, _, err := q.app.Store.Load(q.name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	t, err := target.Get(in.Target)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	var b bytes.Buffer
	if err := t.Logs(in, lines, false, &b); err != nil && b.Len() == 0 {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	q.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	q.w.Write(b.Bytes())
}

// BackupFile is one backup.
type BackupFile struct {
	File    string    `json:"file"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
}

func (s *Server) backups(q *request) {
	list, err := q.app.Backups(q.name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	out := []BackupFile{}
	for _, b := range list {
		out = append(out, BackupFile{File: filepath.Base(b.Path), Created: b.Created, Size: b.Size})
	}
	writeJSON(q.w, http.StatusOK, out)
}

// Result is the body of a successful change.
type Result struct {
	OK  bool   `json:"ok"`
	Log string `json:"log,omitempty"`
}

func (s *Server) action(act string) func(q *request) {
	return func(q *request) {
		a := q.app
		var err error
		switch act {
		case "start":
			err = a.Start(q.name)
		case "stop":
			err = a.Stop(q.name)
		case "restart":
			var warn time.Duration
			if v := q.r.URL.Query().Get("warn"); v != "" {
				if warn, err = time.ParseDuration(v); err != nil {
					fail(q.w, http.StatusBadRequest, fmt.Errorf("warn: %w", err), "")
					return
				}
			}
			err = a.Restart(q.name, warn)
		case "update":
			err = a.Update(q.r.Context(), q.name, 0, false)
		case "backup":
			_, err = a.MakeBackup(q.name)
		}
		if err != nil {
			fail(q.w, http.StatusUnprocessableEntity, err, q.out.String())
			return
		}
		writeJSON(q.w, http.StatusOK, Result{OK: true, Log: q.out.String()})
	}
}

// ConsoleRequest runs one console command.
type ConsoleRequest struct {
	Command string `json:"command"`
}

// ConsoleReply is what the server printed back.
type ConsoleReply struct {
	Reply string `json:"reply"`
}

func (s *Server) console(q *request) {
	var cr ConsoleRequest
	if err := json.NewDecoder(io.LimitReader(q.r.Body, 64<<10)).Decode(&cr); err != nil || strings.TrimSpace(cr.Command) == "" {
		fail(q.w, http.StatusBadRequest, errors.New(`send {"command": "..."}`), "")
		return
	}
	if strings.ContainsAny(cr.Command, "\r\n\x00") {
		fail(q.w, http.StatusBadRequest, errors.New("one line at a time"), "")
		return
	}
	reply, err := q.app.ConsoleCommand(q.name, cr.Command, app.ConsoleOptions{})
	if err != nil {
		fail(q.w, http.StatusUnprocessableEntity, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, ConsoleReply{Reply: reply})
}

// audited logs every change: who, what, and how it went.
func (s *Server) audited(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || s.Audit == nil {
			h.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		h.ServeHTTP(rec, r)
		c, _ := CallerFrom(r.Context())
		who := c.Name
		if c.Local {
			who += " (local)"
		}
		s.auditMu.Lock()
		fmt.Fprintf(s.Audit, "%s %s %s %s %d %s\n", start.UTC().Format(time.RFC3339), who, r.Method, r.URL.RequestURI(), rec.code, time.Since(start).Round(time.Millisecond))
		s.auditMu.Unlock()
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ListenUnix listens on a unix socket only the current user can use,
// replacing a stale socket from an earlier run.
func ListenUnix(p string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", p, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another `anyserve serve` is already listening on %s", p)
	}
	os.Remove(p)
	l, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// ServeLocal serves the API on a unix socket; everyone who can open the
// socket (only the host's user) is the owner.
func (s *Server) ServeLocal(ctx context.Context, l net.Listener) error {
	srv := &http.Server{
		Handler:     s.Handler(),
		BaseContext: func(net.Listener) context.Context { return WithCaller(ctx, app.Owner) },
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// sortEntries sorts folders first, then by name.
func sortEntries(list []FileEntry) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return list[i].Name < list[j].Name
	})
}

// cleanRel turns a request path into a path relative to the instance's
// folder. os.Root does the real confinement; this just normalises and
// rejects obvious nonsense early.
func cleanRel(p string) (string, error) {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if strings.ContainsRune(p, 0) {
		return "", errors.New("bad path")
	}
	rel := strings.TrimPrefix(p, "/")
	if rel == "" {
		rel = "."
	}
	if !fs.ValidPath(rel) {
		return "", errors.New("bad path")
	}
	return rel, nil
}
