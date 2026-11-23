// Package remote is Anyserve's built-in SSH server and its client side.
//
// The server needs no root and no OS accounts: users and their public keys
// live in Anyserve's config. It offers no shell. A connection can only:
//
//   - reach the API, by opening a forwarded connection to api.anyserve:80
//     (each one is an HTTP connection carrying the user's identity);
//   - forward to TCP ports of instances the user may manage;
//   - open an SFTP session limited to those instances' folders.
package remote

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
)

// APIHost is the forwarding address that reaches the API.
const (
	APIHost = "api.anyserve"
	APIPort = 80
)

// Server is the SSH server.
type Server struct {
	// App returns an App for config and instance lookups.
	App func() (*app.App, error)
	// API serves API connections; callers are attached per connection.
	API     http.Handler
	HostKey ssh.Signer
	// AdminCA signs admin certificates (`anyserve users cert`). Admin
	// power remotely needs one; nil means no remote admins.
	AdminCA ssh.PublicKey
	// Audit gets one line per connection, login failure and forward.
	Audit io.Writer
	// Log reports problems.
	Log func(format string, args ...any)

	limiter limiter
	auditMu sync.Mutex
	apiConn chan net.Conn
	once    sync.Once
}

func (s *Server) audit(format string, args ...any) {
	if s.Audit == nil {
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	fmt.Fprintf(s.Audit, "%s ssh "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}

func (s *Server) config(remote net.Addr) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries: 6,
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			a, err := s.App()
			if err != nil {
				return nil, err
			}
			c, err := a.LoadConfig()
			if err != nil {
				return nil, err
			}
			if cert, ok := key.(*ssh.Certificate); ok {
				return s.checkCert(meta, cert, c)
			}
			u, ok := c.UserForKey(key)
			if !ok || u.Name != meta.User() {
				// Clients try several keys, so a miss isn't a failure yet.
				return nil, errors.New("unknown key")
			}
			return &ssh.Permissions{Extensions: map[string]string{"user": u.Name}}, nil
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			if err != nil && method != "none" && method != "publickey" {
				s.audit("%s refused %s login for %q", remote, method, meta.User())
			}
		},
		ServerVersion: "SSH-2.0-Anyserve",
		BannerCallback: func(ssh.ConnMetadata) string {
			return ""
		},
	}
	cfg.AddHostKey(s.HostKey)
	return cfg
}

// checkCert accepts an admin certificate: signed by this host's admin CA,
// a user certificate naming the login user as a principal, within its
// validity, for a user who is still an admin in the config.
func (s *Server) checkCert(meta ssh.ConnMetadata, cert *ssh.Certificate, c *app.Config) (*ssh.Permissions, error) {
	if s.AdminCA == nil {
		return nil, errors.New("admin certificates aren't set up on this host")
	}
	checker := &ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool {
			return bytes.Equal(auth.Marshal(), s.AdminCA.Marshal())
		},
	}
	if _, err := checker.Authenticate(meta, cert); err != nil {
		return nil, err
	}
	u, err := c.FindUser(meta.User())
	if err != nil || !u.Admin {
		return nil, errors.New("certificate for someone who isn't an admin")
	}
	return &ssh.Permissions{Extensions: map[string]string{"user": u.Name, "cert": "admin"}}, nil
}

// Serve accepts SSH connections until ctx ends.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	s.once.Do(s.startAPI)
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handle(ctx, c)
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	ip := hostOf(c.RemoteAddr())
	if !s.limiter.allow(ip) {
		c.Close()
		return
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	conn, chans, reqs, err := ssh.NewServerConn(c, s.config(c.RemoteAddr()))
	if err != nil {
		s.limiter.fail(ip)
		s.audit("%s login failed: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}
	c.SetDeadline(time.Time{})
	s.limiter.ok(ip)
	user := conn.Permissions.Extensions["user"]
	cert := conn.Permissions.Extensions["cert"] == "admin"
	how := "key"
	if cert {
		how = "admin certificate"
	}
	s.audit("%s %s connected with a %s (%s)", c.RemoteAddr(), user, how, conn.ClientVersion())
	defer s.audit("%s %s disconnected", c.RemoteAddr(), user)
	go ssh.DiscardRequests(reqs)
	sess := &session{s: s, conn: conn, user: user, cert: cert}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "direct-tcpip":
			go sess.forward(nc)
		case "session":
			go sess.session(nc)
		default:
			nc.Reject(ssh.UnknownChannelType, "not offered")
		}
	}
}

type session struct {
	s    *Server
	conn *ssh.ServerConn
	user string
	cert bool
}

func (ss *session) caller() (app.Caller, *app.App, error) {
	a, err := ss.s.App()
	if err != nil {
		return app.Caller{}, nil, err
	}
	c, err := a.CallerFor(ss.user, ss.cert)
	return c, a, err
}

// RFC 4254 7.2: host to connect, port, originator address, originator port.
type forwardRequest struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func (ss *session) forward(nc ssh.NewChannel) {
	var req forwardRequest
	if err := ssh.Unmarshal(nc.ExtraData(), &req); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	caller, a, err := ss.caller()
	if err != nil {
		nc.Reject(ssh.Prohibited, "your account was removed")
		return
	}
	if req.Host == APIHost && req.Port == APIPort {
		ch, reqs, err := nc.Accept()
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		ss.s.apiConn <- &chanConn{Channel: ch, caller: caller, local: ss.conn.LocalAddr(), remote: ss.conn.RemoteAddr()}
		return
	}
	switch req.Host {
	case "127.0.0.1", "localhost", "::1":
	default:
		nc.Reject(ssh.Prohibited, "only this host's instance ports can be forwarded")
		return
	}
	inst, ok := PortOwner(a, caller, int(req.Port))
	if !ok {
		ss.s.audit("%s %s refused forward to port %d", ss.conn.RemoteAddr(), ss.user, req.Port)
		nc.Reject(ssh.Prohibited, fmt.Sprintf("port %d isn't a TCP port of an instance you manage", req.Port))
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(req.Port))), 10*time.Second)
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	ss.s.audit("%s %s forwarded to %s port %d", ss.conn.RemoteAddr(), ss.user, inst, req.Port)
	pipe(ch, target)
}

// PortOwner returns the instance owning a TCP port, if the caller may
// manage it.
func PortOwner(a *app.App, c app.Caller, port int) (string, bool) {
	list, _, err := a.Store.List()
	if err != nil {
		return "", false
	}
	for _, in := range list {
		if !a.CanManage(c, in.Name) {
			continue
		}
		_, d, err := a.Store.Load(in.Name)
		if err != nil {
			continue
		}
		ports, err := d.ResolvePorts(in.Ports)
		if err != nil {
			continue
		}
		for _, p := range d.Ports {
			if ports[p.Name] == port && (p.Protocol == "tcp" || p.Protocol == "both" || p.Protocol == "") {
				return in.Name, true
			}
		}
	}
	return "", false
}

func pipe(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst io.WriteCloser, src io.Reader) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// session channels carry only the sftp subsystem; shells and commands are
// refused with a message saying what's on offer.
func (ss *session) session(nc ssh.NewChannel) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			var name struct{ Name string }
			ssh.Unmarshal(req.Payload, &name)
			if name.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			caller, a, err := ss.caller()
			if err != nil {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)
			ss.s.audit("%s %s started sftp", ss.conn.RemoteAddr(), ss.user)
			serveSFTP(ch, a, caller, ss.s.audit, ss.conn.RemoteAddr().String()+" "+ss.user)
			exitStatus(ch, 0)
			return
		case "shell", "exec":
			req.Reply(true, nil)
			fmt.Fprintf(ch.Stderr(), "Anyserve offers no shell. Use `anyserve proxy`, `anyserve port-forward` or SFTP.\r\n")
			exitStatus(ch, 1)
			return
		case "pty-req", "env", "window-change":
			req.Reply(req.Type != "pty-req", nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func exitStatus(ch ssh.Channel, code uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, code)
	ch.SendRequest("exit-status", false, b)
}

// startAPI runs one HTTP server over all API channels.
func (s *Server) startAPI() {
	s.apiConn = make(chan net.Conn)
	l := &chanListener{conns: s.apiConn, done: make(chan struct{})}
	srv := &http.Server{
		Handler:           s.API,
		ReadHeaderTimeout: 30 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if cc, ok := c.(*chanConn); ok {
				return api.WithCaller(ctx, cc.caller)
			}
			return ctx
		},
	}
	go srv.Serve(l)
}

// chanConn makes an SSH channel look like a network connection.
type chanConn struct {
	ssh.Channel
	caller        app.Caller
	local, remote net.Addr
}

func (c *chanConn) LocalAddr() net.Addr                { return c.local }
func (c *chanConn) RemoteAddr() net.Addr               { return c.remote }
func (c *chanConn) SetDeadline(t time.Time) error      { return nil }
func (c *chanConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *chanConn) SetWriteDeadline(t time.Time) error { return nil }

type chanListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}
func (l *chanListener) Addr() net.Addr { return &net.UnixAddr{Name: "anyserve-api", Net: "ssh"} }

func hostOf(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

// limiter blocks addresses with too many failed logins: 10 failures within
// 10 minutes block the address for 15 minutes.
type limiter struct {
	mu   sync.Mutex
	hits map[string]*hit
}

type hit struct {
	fails        int
	first, until time.Time
}

const (
	maxFails   = 10
	failWindow = 10 * time.Minute
	blockFor   = 15 * time.Minute
)

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.hits[ip]
	return h == nil || time.Now().After(h.until)
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string]*hit{}
	}
	now := time.Now()
	h := l.hits[ip]
	if h == nil || now.Sub(h.first) > failWindow {
		h = &hit{first: now}
		l.hits[ip] = h
	}
	h.fails++
	if h.fails >= maxFails {
		h.until = now.Add(blockFor)
	}
	// Forget old entries so the map can't grow without bound.
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if now.Sub(v.first) > failWindow && now.After(v.until) {
				delete(l.hits, k)
			}
		}
	}
}

func (l *limiter) ok(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, ip)
}
