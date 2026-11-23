package spec

import (
	"path/filepath"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestJWTHelper(t *testing.T) {
	ctx := Context{Settings: map[string]any{"secret": "a-very-long-secret-for-testing-only"}}
	tok, err := Render("t", `{{jwt .Settings.secret "anon"}}`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	mac := hmac.New(sha256.New, []byte("a-very-long-secret-for-testing-only"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Error("signature doesn't verify")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil || claims["role"] != "anon" {
		t.Errorf("claims = %s", raw)
	}
	again, _ := Render("t", `{{jwt .Settings.secret "anon"}}`, ctx)
	if again != tok {
		t.Error("jwt should be deterministic")
	}
	if _, err := Render("t", `{{jwt "" "anon"}}`, ctx); err == nil {
		t.Error("an empty secret should be refused")
	}
}

func TestSHA256Helper(t *testing.T) {
	got, err := Render("t", `{{sha256 "abc"}}`, Context{})
	if err != nil || got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %q, %v", got, err)
	}
}

func TestURLHostHelper(t *testing.T) {
	for in, want := range map[string]string{
		"https://chat.example.com/": "chat.example.com",
		"http://localhost:8090":     "localhost:8090",
		"chat.example.com":          "chat.example.com",
	} {
		if got, _ := Render("t", `{{urlHost .Settings.u}}`, Context{Settings: map[string]any{"u": in}}); got != want {
			t.Errorf("urlHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePortsClash(t *testing.T) {
	d := &Definition{Name: "x", Ports: []Port{{Name: "game", Port: 25565, Protocol: "tcp"}, {Name: "rcon", Port: 25575, Protocol: "tcp"}, {Name: "query", Port: 25565, Protocol: "udp"}}}
	if _, err := d.ResolvePorts(nil); err != nil {
		t.Errorf("tcp and udp on one number should be fine: %v", err)
	}
	if _, err := d.ResolvePorts(map[string]int{"game": 25575}); err == nil {
		t.Error("game and rcon on one TCP port wasn't refused")
	}
}

func TestRegistryDefaultPortsDontClash(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "registry", "services", "*.yaml"))
	for _, f := range files {
		d, err := LoadFile(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if _, err := d.ResolvePorts(nil); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}
