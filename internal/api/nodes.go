package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/jonsjava/anyserve/internal/app"
)

// NodeInfo is a worker in this manager's fleet.
type NodeInfo struct {
	Name      string    `json:"name"`
	Joined    time.Time `json:"joined"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitzero"`
	Version   string    `json:"version,omitempty"`
	Addr      string    `json:"addr,omitempty"`
}

// Fleet is the manager's view of its workers (the SSH server).
type Fleet interface {
	Nodes() []NodeInfo
	DialNode(ctx context.Context, name string) (net.Conn, error)
	DropNode(name string)
	// JoinCommand makes a single-use join token and the command a new
	// machine runs with it.
	JoinCommand(a *app.App) (command string, expires time.Time, err error)
}

// JoinInfo is the reply to POST /v1/fleet/token.
type JoinInfo struct {
	Command string    `json:"command"`
	Expires time.Time `json:"expires"`
}

func (s *Server) fleetOr(q *request) (Fleet, bool) {
	if s.Fleet == nil {
		fail(q.w, http.StatusNotFound, errors.New("this host isn't running a fleet manager; turn on remote access (`anyserve remote enable --listen 0.0.0.0:7022`) and run `anyserve serve`"), "")
		return nil, false
	}
	return s.Fleet, true
}

func (s *Server) nodesList(q *request) {
	if s.Fleet == nil {
		writeJSON(q.w, http.StatusOK, []NodeInfo{})
		return
	}
	writeJSON(q.w, http.StatusOK, s.Fleet.Nodes())
}

func (s *Server) fleetToken(q *request) {
	f, ok := s.fleetOr(q)
	if !ok {
		return
	}
	cmd, exp, err := f.JoinCommand(q.app)
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, JoinInfo{Command: cmd, Expires: exp})
}

func (s *Server) nodeRemove(q *request) {
	f, ok := s.fleetOr(q)
	if !ok {
		return
	}
	name := q.r.PathValue("node")
	if err := q.app.RemoveNode(name); err != nil {
		fail(q.w, http.StatusNotFound, err, "")
		return
	}
	f.DropNode(name)
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

// nodeProxy passes /v1/nodes/{node}/v1/... to that node's API. Only
// admins may use it for now; per-node grants come later.
func (s *Server) nodeProxy(q *request) {
	f, ok := s.fleetOr(q)
	if !ok {
		return
	}
	node := q.r.PathValue("node")
	if app.ValidNodeName(node) != nil {
		fail(q.w, http.StatusNotFound, fmt.Errorf("no node %q", node), "")
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "node.anyserve"
			pr.Out.URL.Path = "/" + q.r.PathValue("rest")
			pr.Out.URL.RawPath = ""
			pr.Out.Host = "node.anyserve"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return f.DialNode(ctx, node) },
			// Each request gets its own channel; channels are cheap.
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			fail(w, http.StatusBadGateway, err, "")
		},
	}
	rp.ServeHTTP(q.w, q.r)
}
