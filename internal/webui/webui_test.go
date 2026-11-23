package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	var seen string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		if r.Header.Get(TokenHeader) != "" {
			t.Error("token passed on to the API")
		}
	})
	h := Handler(api, FixedToken("secret"))
	do := func(host, path, token string) int {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		r.Host = host
		if token != "" {
			r.Header.Set(TokenHeader, token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("127.0.0.1:9000", "/api/v1/instances", "secret"); c != 200 || seen != "/v1/instances" {
		t.Errorf("good request: %d %q", c, seen)
	}
	if c := do("127.0.0.1:9000", "/api/v1/instances", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := do("127.0.0.1:9000", "/api/v1/instances", "wrong"); c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	if c := do("evil.example:9000", "/api/v1/instances", "secret"); c != http.StatusMisdirectedRequest {
		t.Errorf("rebinding host: %d", c)
	}
	if c := do("localhost:9000", "/", ""); c != 200 {
		t.Errorf("page: %d", c)
	}
}
