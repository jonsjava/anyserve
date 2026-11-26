package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/spec"
)

func TestParsePortRange(t *testing.T) {
	for in, want := range map[string][2]int{"25565": {25565, 25565}, "27015-27030": {27015, 27030}, " 100 - 200 ": {100, 200}} {
		f, to, err := ParsePortRange(in)
		if err != nil || f != want[0] || to != want[1] {
			t.Errorf("%q: %d-%d %v", in, f, to, err)
		}
	}
	for _, bad := range []string{"", "x", "0", "70000", "200-100", "1-", "-5"} {
		if _, _, err := ParsePortRange(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestReservations(t *testing.T) {
	dir := t.TempDir()
	a := &App{Dirs: paths.Dirs{Config: dir, Data: dir, Cache: dir}, Store: instance.Store{Root: filepath.Join(dir, "instances")}, Log: func(string, ...any) {}}
	// High ports below the range Linux uses for outgoing connections (32768+), so nothing on the test machine is likely to hold them.
	if _, err := a.Reserve(PortReservation{From: 31000, To: 31004, Note: "panel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve(PortReservation{From: 31000, To: 31004, Protocol: "both"}); err == nil {
		t.Error("a duplicate reservation was accepted")
	}
	if _, err := a.Reserve(PortReservation{From: 31010, To: 31010, Protocol: "udp"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []PortReservation{{From: 5, To: 1}, {From: 1, To: 70000}, {From: 1, To: 1, Protocol: "sctp"}, {From: 1, To: 1, Note: "a\nb"}} {
		if _, err := a.Reserve(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}

	d := &spec.Definition{Name: "g", Ports: []spec.Port{
		{Name: "game", Port: 31002, Protocol: "udp"},
		{Name: "rcon", Port: 31010, Protocol: "tcp"}, // only UDP is reserved there
	}}
	ports, _ := d.ResolvePorts(nil)
	moved := a.pickFreePorts("new", d, nil, ports)
	if ports["game"] != 31005 {
		t.Errorf("game should skip the reserved 31000-31004 to 31005: %v", ports)
	}
	if ports["rcon"] != 31010 {
		t.Errorf("rcon moved off a port reserved only for UDP: %v", ports)
	}
	if len(moved) != 1 {
		t.Errorf("moves: %v", moved)
	}
	// Choosing a reserved port yourself works, with a warning.
	ports, _ = d.ResolvePorts(map[string]int{"game": 31003})
	a.pickFreePorts("new", d, map[string]int{"game": 31003}, ports)
	if ports["game"] != 31003 {
		t.Errorf("the user's port moved: %v", ports)
	}
	_, warnings := a.checkPorts("new", d, ports)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "31000-31004/both: panel") {
		t.Errorf("warnings: %v", warnings)
	}

	if _, err := a.Unreserve(31000, 31003); err == nil || !strings.Contains(err.Error(), "31000-31004/both") {
		t.Errorf("removing a range that isn't reserved: %v", err)
	}
	if n, err := a.Unreserve(31000, 31004); err != nil || n != 1 {
		t.Fatalf("unreserve: %d %v", n, err)
	}
	rs, _ := a.Reservations()
	if len(rs) != 1 || rs[0].String() != "31010/udp" {
		t.Errorf("left: %v", rs)
	}
}
