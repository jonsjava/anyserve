package https

import (
	"strings"
	"testing"
)

var sites = []Site{{Domain: "vault.example.com", Port: 8222, Instance: "vault"}, {Domain: "chat.example.com", Port: 8090, Instance: "harmony"}}

func TestCaddyfile(t *testing.T) {
	c := Caddyfile("ops@example.com", sites)
	for _, want := range []string{"admin unix//config/admin.sock", "email ops@example.com", "vault.example.com {\n\tencode zstd gzip\n\treverse_proxy 127.0.0.1:8222\n}", "chat.example.com {"} {
		if !strings.Contains(c, want) {
			t.Errorf("Caddyfile lacks %q:\n%s", want, c)
		}
	}
	if strings.Index(c, "chat.") > strings.Index(c, "vault.") {
		t.Error("sites aren't sorted")
	}
	if strings.Contains(Caddyfile("", nil), "email") {
		t.Error("empty email written")
	}
}

func TestNginxAndTraefik(t *testing.T) {
	n := NginxConf(sites)
	for _, want := range []string{"server_name vault.example.com;", "proxy_pass http://127.0.0.1:8222;", "/etc/letsencrypt/live/vault.example.com/fullchain.pem", "$connection_upgrade"} {
		if !strings.Contains(n, want) {
			t.Errorf("nginx lacks %q", want)
		}
	}
	tr := TraefikConf(sites)
	for _, want := range []string{"rule: \"Host(`vault.example.com`)\"", "url: \"http://127.0.0.1:8222\"", "certResolver: letsencrypt"} {
		if !strings.Contains(tr, want) {
			t.Errorf("traefik lacks %q:\n%s", want, tr)
		}
	}
	if !strings.Contains(TraefikConf(nil), "http: {}") {
		t.Error("empty traefik config isn't valid")
	}
}

func TestValidDomain(t *testing.T) {
	for _, ok := range []string{"vault.example.com", "a.b.co", "x-1.example.org"} {
		if ValidDomain(ok) != nil {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "localhost", "-a.example.com", "a..com", "Vault.Example.com", "a.com\n b.com", "a.com {", "a.com/x", "*.example.com"} {
		if ValidDomain(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCaddyCompose(t *testing.T) {
	c := CaddyCompose(1001, 1001)
	for _, want := range []string{CaddyImage, "network_mode: host", `user: "1001:1001"`, "cap_drop: [ALL]", "cap_add: [NET_BIND_SERVICE]", "read_only: true"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
}

func TestRouter(t *testing.T) {
	b, err := RouterRoutes(map[string]int{"b.example.com": 25566, "a.example.com": 25567})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"mappings\": {\n    \"a.example.com\": \"127.0.0.1:25567\",\n    \"b.example.com\": \"127.0.0.1:25566\"\n  }\n}\n"
	if string(b) != want {
		t.Errorf("routes:\n%s", b)
	}
	c := RouterCompose(1000, 1000, 25565)
	for _, s := range []string{RouterImage, `user: "1000:1000"`, "read_only: true", "cap_drop: [ALL]", `"-port", "25565"`, "network_mode: host"} {
		if !strings.Contains(c, s) {
			t.Errorf("compose lacks %q", s)
		}
	}
}

func TestRouterDropped(t *testing.T) {
	before := map[string]string{"a.example.com": "127.0.0.1:25566"}
	if dropped(before, map[string]int{"a.example.com": 25566, "b.example.com": 25567}) {
		t.Error("an added route needs no restart")
	}
	if !dropped(before, map[string]int{"b.example.com": 25567}) {
		t.Error("a removed route needs a restart")
	}
	if !dropped(before, map[string]int{"a.example.com": 25570}) {
		t.Error("a moved route needs a restart")
	}
}
