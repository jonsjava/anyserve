package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/spec"
)

func TestPatchINI(t *testing.T) {
	in := "[URL]\r\nPort=7777\r\n; Port=1\r\n\r\n[/Script/Engine.GameSession]\r\nMaxPlayers=8\r\n"
	got := patchINI(in, map[string]string{
		"URL.Port":                                 "9000",
		"/Script/Engine.GameSession.MaxPlayers":    "20",
		"/Script/Engine.GameSession.ServerName":    "Hi",
		"OnlineSubsystemSteam.GameServerQueryPort": "27015",
	})
	want := "[URL]\r\nPort=9000\r\n; Port=1\r\n\r\n[/Script/Engine.GameSession]\r\nMaxPlayers=20\r\nServerName=Hi\r\n\r\n[OnlineSubsystemSteam]\r\nGameServerQueryPort=27015\r\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestPatchLines(t *testing.T) {
	in := "server_config : .config {\n connection_dedicated_port: 100\n lobby_name: \"x\"\n}\n"
	got := patchLines(in, map[string]string{
		" connection_dedicated_port:": " connection_dedicated_port: 27015",
		" lobby_name:":                " lobby_name: \"Mine\"",
	})
	want := "server_config : .config {\n connection_dedicated_port: 27015\n lobby_name: \"Mine\"\n}\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestPatchJSONKeepsTypes(t *testing.T) {
	got, err := patchJSON(`{"worldName":"a","maxPlayers":10,"pvp":false,"keep":{"x":1}}`, map[string]string{
		"worldName": "123", "maxPlayers": "20", "pvp": "true", "keep.y": "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"worldName": "123"`, `"maxPlayers": 20`, `"pvp": true`, `"x": 1`, `"y": 2`} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %s in %s", s, got)
		}
	}
}

func TestWriteFilesSetWaitsForGameFile(t *testing.T) {
	dir := t.TempDir()
	d := &spec.Definition{Files: []spec.File{
		{Path: "cfg/Game.ini", Format: "ini", Set: map[string]string{"S.Port": "{{.Ports.game}}"}},
	}}
	ctx := spec.Context{OS: "linux", Ports: map[string]int{"game": 5}}
	written, pending, err := WriteFiles(d, dir, ctx)
	if err != nil || len(written) != 0 || len(pending) != 1 {
		t.Fatalf("written=%v pending=%v err=%v", written, pending, err)
	}
	os.MkdirAll(filepath.Join(dir, "cfg"), 0o755)
	os.WriteFile(filepath.Join(dir, "cfg/Game.ini"), []byte("[S]\nPort=1\nOther=x\n"), 0o644)
	if _, _, err := WriteFiles(d, dir, ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cfg/Game.ini")); string(b) != "[S]\nPort=5\nOther=x\n" {
		t.Errorf("got %q", b)
	}
}
