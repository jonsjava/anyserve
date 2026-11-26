package app

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

func loadDef(t *testing.T, id string) *spec.Definition {
	t.Helper()
	d, err := spec.LoadFile(filepath.Join("..", "..", "registry", "services", id+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAddresses(t *testing.T) {
	old := hostAddress
	hostAddress = func() string { return "203.0.113.7" }
	defer func() { hostAddress = old }()

	d := loadDef(t, "harmony")
	ports := map[string]int{"http": 8090, "api": 8091}

	// No domain: both ports open, and the addresses use this machine's.
	if got := defaultExpose(d, "", ""); !reflect.DeepEqual(got, []string{"http", "api"}) {
		t.Errorf("expose without a domain: %v", got)
	}
	settings := map[string]any{}
	filled := fillAddresses(d, map[string]string{}, settings, ports, "", "")
	if !reflect.DeepEqual(filled, []string{"url", "api_url"}) || settings["url"] != "http://203.0.113.7:8090" || settings["api_url"] != "http://203.0.113.7:8091" {
		t.Errorf("filled %v: %v", filled, settings)
	}
	// What the user gives is kept.
	settings = map[string]any{"url": "https://chat.example.com"}
	if filled := fillAddresses(d, map[string]string{"url": "https://chat.example.com"}, settings, ports, "", ""); !reflect.DeepEqual(filled, []string{"api_url"}) || settings["url"] != "https://chat.example.com" {
		t.Errorf("user's url: %v %v", filled, settings)
	}

	// A domain: the proxy serves http, which stays on this machine.
	if got := defaultExpose(d, "chat.example.com", ""); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("expose with a domain: %v", got)
	}
	if u := PortURL(d, "http", ports, "chat.example.com", ""); u != "https://chat.example.com" {
		t.Errorf("proxied url: %s", u)
	}

	// Host form and a path.
	g := loadDef(t, "gitea")
	gp := map[string]int{"http": 3000, "ssh": 2222}
	settings = map[string]any{}
	fillAddresses(g, nil, settings, gp, "git.example.com", "")
	if settings["domain"] != "git.example.com" || settings["root_url"] != "https://git.example.com/" {
		t.Errorf("gitea with a domain: %v", settings)
	}
	settings = map[string]any{}
	fillAddresses(g, nil, settings, gp, "", "")
	if settings["domain"] != "203.0.113.7" || settings["root_url"] != "http://203.0.113.7:3000/" {
		t.Errorf("gitea without a domain: %v", settings)
	}

	// What people are told to open.
	in := &instance.Instance{Expose: []string{"http", "ssh"}}
	want := [][2]string{{"http", "http://203.0.113.7:3000"}, {"ssh", "ssh://203.0.113.7:2222"}}
	if got := Addresses(in, g, gp); !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: %v", got)
	}
	if got := Addresses(&instance.Instance{}, g, gp); len(got) != 0 {
		t.Errorf("private ports listed: %v", got)
	}
}

func TestRefreshAddresses(t *testing.T) {
	old := hostAddress
	hostAddress = func() string { return "203.0.113.7" }
	defer func() { hostAddress = old }()

	d := loadDef(t, "harmony")
	in := &instance.Instance{
		Settings:    map[string]string{"url": "http://203.0.113.7:8090", "api_url": "http://203.0.113.7:8091"},
		Ports:       map[string]int{"http": 9000, "api": 8091},
		AutoAddress: []string{"url", "api_url"},
	}
	// The http port moved: url follows it.
	if changed := refreshAddresses(in, d, nil); !reflect.DeepEqual(changed, []string{"url"}) || in.Settings["url"] != "http://203.0.113.7:9000" {
		t.Errorf("after a port change: %v %v", changed, in.Settings)
	}
	// Once the user sets one, it's theirs.
	in.Settings["api_url"] = "https://db.example.com"
	in.Ports["api"] = 9001
	refreshAddresses(in, d, map[string]string{"api_url": "https://db.example.com"})
	if in.Settings["api_url"] != "https://db.example.com" || !reflect.DeepEqual(in.AutoAddress, []string{"url"}) {
		t.Errorf("user's api_url: %v %v", in.Settings, in.AutoAddress)
	}

	// once: filled at install, left alone after.
	l := loadDef(t, "lemmy")
	li := &instance.Instance{Settings: map[string]string{"hostname": "203.0.113.7"}, Ports: map[string]int{"http": 10633}, Domain: "lemmy.example.com", AutoAddress: []string{"hostname"}}
	if changed := refreshAddresses(li, l, nil); len(changed) != 0 || li.Settings["hostname"] != "203.0.113.7" {
		t.Errorf("once setting changed: %v %v", changed, li.Settings)
	}
}
