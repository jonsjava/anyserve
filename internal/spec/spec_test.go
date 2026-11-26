package spec

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryDefinitionsAreValid(t *testing.T) {
	files, err := filepath.Glob("../../registry/services/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no definitions found: %v", err)
	}
	for _, f := range files {
		d, err := LoadFile(f)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".yaml"); d.ID != want {
			t.Errorf("%s: id %q should match the file name %q", f, d.ID, want)
		}
		// Every native platform must render with default settings, plus a
		// stand-in for required settings that have no default (tokens).
		raw := map[string]string{}
		for _, s := range d.Settings {
			if s.Default == nil && !s.Optional && !s.Generate && s.Type == "string" {
				raw[s.Key] = "stand-in"
			}
			if s.MustAccept {
				raw[s.Key] = "true"
			}
			if s.Type == "folder" {
				// Defaults like ~/Music may not exist on this machine.
				raw[s.Key] = t.TempDir()
			}
		}
		settings, err := d.ResolveSettings(raw)
		if err != nil {
			t.Errorf("%s: default settings: %v", d.ID, err)
			continue
		}
		ports, _ := d.ResolvePorts(nil)
		ctx := Context{Name: "test", Dir: "/srv/test", Settings: settings, Ports: ports}
		if d.IsStack() {
			if _, err := d.RenderStack(ctx); err != nil {
				t.Errorf("%s: render stack: %v", d.ID, err)
			}
			for _, f := range d.Files {
				if _, err := Render(f.Path, f.Template, ctx); err != nil {
					t.Errorf("%s: render %s: %v", d.ID, f.Path, err)
				}
			}
		}
		for _, goos := range d.Platforms {
			ctx.OS = goos
			if _, err := d.RenderRun(goos, ctx); err != nil {
				t.Errorf("%s: render run for %s: %v", d.ID, goos, err)
			}
			for _, f := range d.Files {
				if _, err := Render(f.Path, f.Template, ctx); err != nil {
					t.Errorf("%s: render %s: %v", d.ID, f.Path, err)
				}
				for k, v := range f.Set {
					if _, err := Render(f.Path, v, ctx); err != nil {
						t.Errorf("%s: render %s[%s]: %v", d.ID, f.Path, k, err)
					}
				}
			}
		}
	}
}

const minimal = `
schema: 1
id: demo
name: Demo
description: A demo.
version: "1"
platforms: [linux]
install:
  archive:
    url: https://example.com/demo.zip
ports:
  - { name: game, port: 1234, protocol: tcp, public: true }
run:
  linux:
    command: demo
`

func TestMinimalIsValid(t *testing.T) {
	d, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsUnsafeAndUnknown(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"path escape":  {"command: demo", "command: ../../bin/sh", "inside the instance directory"},
		"absolute":     {"command: demo", "command: /bin/sh", "relative"},
		"http":         {"https://example.com", "http://example.com", "https://"},
		"bad step":     {"ports:", "steps:\n  - { type: shell, path: x }\nports:", "unknown step type"},
		"step escape":  {"ports:", "steps:\n  - { type: touch, path: ../x }\nports:", "inside the instance directory"},
		"missing run":  {"platforms: [linux]", "platforms: [linux, windows]", `no entry for platform "windows"`},
		"bad port":     {"port: 1234", "port: 70000", "1-65535"},
		"address port": {"ports:", "settings:\n  - { key: url, prompt: URL, type: string, address: { port: web } }\nports:", `address port "web" isn't one of the ports`},
		"address type": {"ports:", "settings:\n  - { key: url, prompt: URL, type: int, address: { port: game } }\nports:", "address only works with string settings"},
		"address form": {"ports:", "settings:\n  - { key: url, prompt: URL, type: string, address: { port: game, form: path } }\nports:", "form must be url or host"},
		"address path": {"ports:", "settings:\n  - { key: url, prompt: URL, type: string, address: { port: game, form: host, path: / } }\nports:", "needs the url form"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := Parse([]byte(strings.Replace(minimal, c.old, c.new, 1)))
			if err == nil {
				err = d.Validate()
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	if _, err := Parse([]byte(minimal + "\nshell: rm -rf /\n")); err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("want unknown field error, got %v", err)
	}
	nested := strings.Replace(minimal, "command: demo", "command: demo\n    hook: x", 1)
	if _, err := Parse([]byte(nested)); err == nil {
		t.Fatal("want error for unknown nested field")
	}
}

func TestEmptyArgsDropped(t *testing.T) {
	d, err := LoadFile("../../registry/services/rust.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.ResolveSettings(map[string]string{"rcon_password": "x"})
	p, _ := d.ResolvePorts(nil)
	r, err := d.RenderRun("linux", Context{Dir: "/d", Settings: s, Ports: p})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range r.Args {
		if a == "" || a == "+server.description" {
			t.Fatalf("empty description should drop both the flag and value: %q", r.Args)
		}
	}
}

func TestSettingConversion(t *testing.T) {
	d, _ := LoadFile("../../registry/services/terraria.yaml")
	if _, err := d.ResolveSettings(map[string]string{"max_players": "999"}); err == nil {
		t.Error("want max error")
	}
	if _, err := d.ResolveSettings(map[string]string{"nope": "1"}); err == nil {
		t.Error("want unknown setting error")
	}
	if _, err := d.ResolveSettings(map[string]string{"motd": "a\nb"}); err == nil {
		t.Error("want single-line error")
	}
	got, err := d.ResolveSettings(map[string]string{"max_players": "16", "world_size": "3"})
	if err != nil || got["max_players"] != 16 || got["world_size"] != "3" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestRequiresMustBeAVersion(t *testing.T) {
	d := &Definition{Schema: 1, ID: "x", Name: "X", Description: "d", Version: "1", Kind: KindStack, Requires: "soon"}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "requires must be") {
		t.Errorf("want a requires error, got %v", err)
	}
}
