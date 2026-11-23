package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/selfupdate"
	"github.com/jonsjava/anyserve/internal/version"
)

// SSH user names with a special meaning: a worker joining with a token,
// and a joined worker keeping its connection open.
const (
	JoinUser = "anyserve-join"
	NodeUser = "anyserve-node"
	// nodeAPIChannel is opened by the manager to reach a worker's API.
	nodeAPIChannel = "anyserve-node-api"
	joinSubsystem  = "anyserve-join"
)

// authorized renders a public key as one authorized_keys line.
func authorized(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

type joinRequest struct {
	Token   string `json:"token"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type joinReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// nodeConn is a connected worker.
type nodeConn struct {
	conn    *ssh.ServerConn
	since   time.Time
	version string
	addr    string
}

type fleet struct {
	mu    sync.Mutex
	nodes map[string]*nodeConn
}

// handleJoin serves a worker joining with a token: one session whose
// anyserve-join subsystem carries the token and the worker's name.
func (s *Server) handleJoin(conn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) {
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	key := conn.Permissions.Extensions["key"]
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only joining is offered")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		ok := false
		for r := range creqs {
			if r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == joinSubsystem {
				r.Reply(true, nil)
				ok = true
				break
			}
			r.Reply(false, nil)
		}
		if !ok {
			ch.Close()
			return
		}
		var jr joinRequest
		line, err := bufio.NewReader(io.LimitReader(ch, 4096)).ReadBytes('\n')
		if err == nil {
			err = json.Unmarshal(line, &jr)
		}
		reply := joinReply{OK: true}
		if err == nil {
			var a *app.App
			if a, err = s.App(); err == nil {
				secret, _, serr := app.SplitJoinToken(jr.Token)
				if serr != nil {
					err = serr
				} else {
					err = a.Enroll(secret, jr.Name, key)
				}
			}
		}
		if err != nil {
			reply = joinReply{Error: err.Error()}
			s.audit("%s fleet join as %q refused: %v", conn.RemoteAddr(), jr.Name, err)
		} else {
			s.audit("%s fleet node %s joined (Anyserve %s)", conn.RemoteAddr(), jr.Name, jr.Version)
		}
		b, _ := json.Marshal(reply)
		ch.Write(append(b, '\n'))
		exitStatus(ch, 0)
		ch.Close()
		return
	}
}

// handleNode keeps a joined worker's connection, through which the
// manager reaches its API. Workers can't open anything on the manager.
func (s *Server) handleNode(conn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) {
	name := conn.Permissions.Extensions["node"]
	ver := strings.TrimPrefix(string(conn.ClientVersion()), "SSH-2.0-Anyserve_")
	nc := &nodeConn{conn: conn, since: time.Now().UTC(), version: ver, addr: conn.RemoteAddr().String()}
	s.fleet.mu.Lock()
	if s.fleet.nodes == nil {
		s.fleet.nodes = map[string]*nodeConn{}
	}
	if old := s.fleet.nodes[name]; old != nil {
		old.conn.Close()
	}
	s.fleet.nodes[name] = nc
	s.fleet.mu.Unlock()
	s.audit("%s fleet node %s connected (Anyserve %s)", conn.RemoteAddr(), name, ver)
	go func() {
		for r := range reqs {
			// Keepalives.
			if r.WantReply {
				r.Reply(r.Type == "keepalive@anyserve", nil)
			}
		}
	}()
	for c := range chans {
		c.Reject(ssh.Prohibited, "workers can't open channels on the manager")
	}
	s.fleet.mu.Lock()
	if s.fleet.nodes[name] == nc {
		delete(s.fleet.nodes, name)
	}
	s.fleet.mu.Unlock()
	s.audit("%s fleet node %s disconnected", conn.RemoteAddr(), name)
}

// Nodes lists the joined nodes and whether each is connected.
func (s *Server) Nodes() []api.NodeInfo {
	a, err := s.App()
	if err != nil {
		return nil
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return nil
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	out := []api.NodeInfo{}
	for _, n := range cfg.Fleet.Nodes {
		ni := api.NodeInfo{Name: n.Name, Joined: n.Joined}
		if c := s.fleet.nodes[n.Name]; c != nil {
			ni.Connected, ni.Since, ni.Version, ni.Addr = true, c.since, c.version, c.addr
		}
		out = append(out, ni)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DialNode opens a connection to a node's API.
func (s *Server) DialNode(ctx context.Context, name string) (net.Conn, error) {
	s.fleet.mu.Lock()
	nc := s.fleet.nodes[name]
	s.fleet.mu.Unlock()
	if nc == nil {
		return nil, fmt.Errorf("node %s isn't connected", name)
	}
	ch, reqs, err := nc.conn.OpenChannel(nodeAPIChannel, nil)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", name, err)
	}
	go ssh.DiscardRequests(reqs)
	return &chanConn{Channel: ch, local: nc.conn.LocalAddr(), remote: nc.conn.RemoteAddr()}, nil
}

// DropNode disconnects a node, e.g. after it's removed.
func (s *Server) DropNode(name string) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if nc := s.fleet.nodes[name]; nc != nil {
		nc.conn.Close()
		delete(s.fleet.nodes, name)
	}
}

func clientVersion() string {
	return "SSH-2.0-Anyserve_" + strings.Map(func(r rune) rune {
		if r <= ' ' || r == '-' {
			return '_'
		}
		return r
	}, version.Current())
}

// Join enrolls this machine with a manager. fingerprint is the manager's
// host key fingerprint from the token; it returns the manager's host key
// to pin.
func Join(addr, token, name string, key ssh.Signer) (string, error) {
	_, fp, err := app.SplitJoinToken(token)
	if err != nil {
		return "", err
	}
	var hostKey string
	cfg := &ssh.ClientConfig{
		User: JoinUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if Fingerprint(k) != fp {
				return fmt.Errorf("%s isn't the manager that made this token (its key is %s, the token expects %s)", addr, Fingerprint(k), fp)
			}
			hostKey = authorized(k)
			return nil
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		ClientVersion:     clientVersion(),
		Timeout:           15 * time.Second,
	}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", fmt.Errorf("connecting to the manager at %s: %w", addr, err)
	}
	defer c.Close()
	sess, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	in, _ := sess.StdinPipe()
	out, _ := sess.StdoutPipe()
	if err := sess.RequestSubsystem(joinSubsystem); err != nil {
		return "", fmt.Errorf("the manager doesn't offer joining (is its Anyserve up to date?): %w", err)
	}
	b, _ := json.Marshal(joinRequest{Token: token, Name: name, Version: version.Current()})
	if _, err := in.Write(append(b, '\n')); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(out, 4096)).ReadBytes('\n')
	if err != nil {
		return "", fmt.Errorf("the manager closed the connection: %w", err)
	}
	var r joinReply
	if err := json.Unmarshal(line, &r); err != nil {
		return "", err
	}
	if !r.OK {
		return "", errors.New(r.Error)
	}
	return hostKey, nil
}

// RunAgent keeps this worker connected to its manager and serves the
// manager's requests with handler, reconnecting until ctx ends.
func RunAgent(ctx context.Context, link app.ManagerLink, key ssh.Signer, handler http.Handler, logf func(string, ...any)) error {
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(link.HostKey))
	if err != nil {
		return fmt.Errorf("the manager's pinned key is unreadable; join again: %w", err)
	}
	conns := make(chan net.Conn)
	l := &chanListener{conns: conns, done: make(chan struct{})}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		// The manager acts as this machine's owner; it checks its own
		// users' permissions before passing a request on.
		BaseContext: func(net.Listener) context.Context {
			return api.WithCaller(ctx, app.Caller{Name: "manager", Admin: true, Local: true})
		},
	}
	go srv.Serve(l)
	defer srv.Close()
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := agentOnce(ctx, link, key, pinned, conns)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrHostKeyChanged) {
			return err
		}
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		if err != nil && strings.Contains(err.Error(), "unable to authenticate") {
			// The manager is up but no longer knows this machine: it was
			// removed. Keep trying slowly in case that's undone.
			backoff = time.Minute
			logf("The manager at %s no longer accepts this machine (it was removed from the fleet). To rejoin: `anyserve fleet leave`, then `anyserve fleet join` with a new token. Retrying in %s", link.Addr, backoff)
		} else {
			logf("Lost the manager (%v); retrying in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	return nil
}

func agentOnce(ctx context.Context, link app.ManagerLink, key ssh.Signer, pinned ssh.PublicKey, conns chan<- net.Conn) error {
	cfg := &ssh.ClientConfig{
		User: NodeUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if authorized(k) != authorized(pinned) {
				return fmt.Errorf("%w: the manager's key is now %s", ErrHostKeyChanged, Fingerprint(k))
			}
			return nil
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		ClientVersion:     clientVersion(),
		Timeout:           15 * time.Second,
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", link.Addr)
	if err != nil {
		return err
	}
	conn, chans, reqs, err := ssh.NewClientConn(nc, link.Addr, cfg)
	if err != nil {
		nc.Close()
		return err
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-done:
				return
			case <-t.C:
				if _, _, err := conn.SendRequest("keepalive@anyserve", true, nil); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	for c := range chans {
		if c.ChannelType() != nodeAPIChannel {
			c.Reject(ssh.UnknownChannelType, "not offered")
			continue
		}
		ch, creqs, err := c.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		select {
		case conns <- &chanConn{Channel: ch, local: conn.LocalAddr(), remote: conn.RemoteAddr()}:
		case <-ctx.Done():
			ch.Close()
		}
	}
	return conn.Wait()
}

// JoinCommand makes a single-use join token and the one command a new
// machine runs to install Anyserve and join this manager.
func (s *Server) JoinCommand(a *app.App) (string, time.Time, error) {
	addr, err := a.ManagerAddr()
	if err != nil {
		return "", time.Time{}, err
	}
	tok, exp, err := a.NewJoinToken(Fingerprint(s.HostKey.PublicKey()))
	if err != nil {
		return "", time.Time{}, err
	}
	return JoinCommandFor(addr, tok), exp, nil
}

// JoinCommandFor is the install-and-join command for a token.
func JoinCommandFor(addr, token string) string {
	return fmt.Sprintf("curl -fsSL https://github.com/%s/releases/latest/download/install.sh | sh -s -- fleet join %s %s", selfupdate.Repo, addr, token)
}
