package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/webui"
)

// WebSession is what POST /v1/web-session returns: a token for the web UI,
// bound to the user who asked for it.
type WebSession struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
	// CA is the web CA certificate (PEM) the browser should trust.
	CA string `json:"ca"`
}

// sessionTTL is how long a web UI token works.
const sessionTTL = 12 * time.Hour

type sessions struct {
	mu sync.Mutex
	m  map[string]sessionEntry
}

type sessionEntry struct {
	user    string
	expires time.Time
}

func (ss *sessions) issue(user string) (string, time.Time) {
	b := make([]byte, 24)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	exp := time.Now().Add(sessionTTL)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.m == nil {
		ss.m = map[string]sessionEntry{}
	}
	for k, v := range ss.m {
		if time.Now().After(v.expires) {
			delete(ss.m, k)
		}
	}
	ss.m[tok] = sessionEntry{user: user, expires: exp}
	return tok, exp
}

// valid reports whether a token was issued to user and hasn't expired.
func (ss *sessions) valid(token, user string) bool {
	if token == "" {
		return false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for k, v := range ss.m {
		if subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			return v.user == user && time.Now().Before(v.expires)
		}
	}
	return false
}

// apiRoutes adds the remote-only routes to the API.
func (s *Server) apiRoutes() http.Handler {
	m := http.NewServeMux()
	m.Handle("/", s.API)
	m.HandleFunc("POST /v1/web-session", func(w http.ResponseWriter, r *http.Request) {
		c, ok := api.CallerFrom(r.Context())
		if !ok || s.WebCA == nil {
			http.Error(w, `{"error":"the web UI isn't available on this host"}`, http.StatusNotFound)
			return
		}
		tok, exp := s.sessions.issue(c.Name)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WebSession{Token: tok, Expires: exp, CA: string(s.WebCA.PEM())})
	})
	return m
}

// startWeb serves the web UI over TLS inside the SSH channels. TLS ends
// here on the host, so the browser's client certificate is checked by the
// host, not by the user's own (possibly modified) client.
func (s *Server) startWeb() error {
	cfg, err := s.WebCA.TLSConfig()
	if err != nil {
		return err
	}
	s.webConn = make(chan net.Conn)
	l := tls.NewListener(&chanListener{conns: s.webConn, done: make(chan struct{})}, cfg)
	allow := func(r *http.Request, token string) bool {
		c, ok := api.CallerFrom(r.Context())
		return ok && s.sessions.valid(token, c.Name)
	}
	srv := &http.Server{
		Handler:           webui.Handler(s.API, allow),
		ReadHeaderTimeout: 30 * time.Second,
		// The certificate is checked per connection; a short idle limit
		// means a revoked one stops working within minutes.
		IdleTimeout: 2 * time.Minute,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			tc, ok := c.(*tls.Conn)
			if !ok {
				return ctx
			}
			cc, ok := tc.NetConn().(*chanConn)
			if !ok {
				return ctx
			}
			hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := tc.HandshakeContext(hctx); err != nil {
				return ctx // no caller: every request is refused
			}
			return api.WithCaller(ctx, s.webCaller(cc.caller.Name, tc.ConnectionState()))
		},
	}
	go srv.Serve(l)
	return nil
}

// webCaller decides a web connection's caller: admin power only with a
// client certificate issued to this user and not revoked.
func (s *Server) webCaller(user string, state tls.ConnectionState) app.Caller {
	a, err := s.App()
	if err != nil {
		return app.Caller{Name: user}
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return app.Caller{Name: user}
	}
	u, err := cfg.FindUser(user)
	if err != nil {
		return app.Caller{Name: user}
	}
	cert := CertFor(state, user, u.CertSerials)
	if cert {
		s.audit("%s presented a browser client certificate", user)
	}
	c, err := a.CallerFor(user, cert)
	if err != nil {
		return app.Caller{Name: user}
	}
	return c
}
