// Package webui is the browser UI that `anyserve proxy` serves on the
// user's own computer. It's one static page that talks to the API through
// the proxy; there's no build step.
package webui

import (
	"crypto/subtle"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// TokenHeader carries the per-run token on API requests. Browsers won't
// send a custom header cross-origin without a preflight, which the proxy
// never approves, so other websites can't drive the API.
const TokenHeader = "X-Anyserve-Token"

// Handler serves the UI at / and passes /api/... (minus the prefix) to
// api, for requests whose token allow accepts and that address the server
// by a loopback name (which stops DNS-rebinding attacks).
func Handler(api http.Handler, allow func(r *http.Request, token string) bool) http.Handler {
	sub, _ := fs.Sub(static, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "use 127.0.0.1 or localhost", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !allow(r, r.Header.Get(TokenHeader)) {
				http.Error(w, `{"error":"missing or wrong token; open the address anyserve proxy printed"}`, http.StatusUnauthorized)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
			r2.URL.RawPath = ""
			r2.Header.Del(TokenHeader)
			r2.Header.Del("Cookie")
			api.ServeHTTP(w, r2)
			return
		}
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}

// FixedToken accepts one token, for the host's own local UI.
func FixedToken(token string) func(*http.Request, string) bool {
	return func(_ *http.Request, got string) bool {
		return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
}

func loopbackHost(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}
