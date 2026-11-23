package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	"github.com/jonsjava/anyserve/internal/app"
)

// sockClient talks to the running `anyserve serve` over its socket.
type sockClient struct {
	tr *http.Transport
	c  *http.Client
}

// localAPI connects to the running service's API, if there is one.
func localAPI(a *app.App) (*sockClient, error) {
	sock := a.Dirs.Socket()
	if _, err := os.Stat(sock); err != nil {
		return nil, err
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	c := &sockClient{tr: tr, c: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
	// Is anyone listening?
	resp, err := c.c.Get("http://local.anyserve/v1/whoami")
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return c, nil
}

func (s *sockClient) get(path string, v any) error {
	return s.do("GET", path, v)
}

func (s *sockClient) do(method, path string, v any) error {
	req, _ := http.NewRequest(method, "http://local.anyserve"+path, nil)
	resp, err := s.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("%s", e.Error)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// proxy passes requests to the running service's API, unchanged.
func (s *sockClient) proxy() http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "http", "local.anyserve", "local.anyserve"
		},
		Transport: s.tr,
	}
}
