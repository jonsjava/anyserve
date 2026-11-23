package app

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/spec"
)

func TestPickFreePorts(t *testing.T) {
	dir := t.TempDir()
	a := &App{Dirs: paths.Dirs{Config: dir, Data: dir, Cache: dir}, Store: instance.Store{Root: filepath.Join(dir, "instances")}, Log: func(string, ...any) {}}
	// Something on this machine holds a TCP port.
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busyPort := l.Addr().(*net.TCPAddr).Port
	d := &spec.Definition{Name: "g", Ports: []spec.Port{
		{Name: "game", Port: busyPort, Protocol: "tcp"},
		{Name: "query", Port: busyPort, Protocol: "udp"},
		{Name: "rcon", Port: busyPort + 1, Protocol: "tcp"},
	}}
	ports, _ := d.ResolvePorts(nil)
	moved := a.pickFreePorts("new", d, nil, ports)
	if ports["game"] == busyPort || ports["game"] != ports["query"] {
		t.Errorf("game should move off %d with query alongside: %v", busyPort, ports)
	}
	if ports["rcon"] == ports["game"] {
		t.Errorf("rcon clashes with game: %v", ports)
	}
	if len(moved) < 2 {
		t.Errorf("moves not reported: %v", moved)
	}
	// A port the user chose stays put, even when busy.
	ports, _ = d.ResolvePorts(map[string]int{"game": busyPort})
	a.pickFreePorts("new", d, map[string]int{"game": busyPort}, ports)
	if ports["game"] != busyPort {
		t.Errorf("the user's port moved: %v", ports)
	}
}
