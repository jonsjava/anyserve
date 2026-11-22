package target

import (
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

func TestSystemdUnit(t *testing.T) {
	s := &systemd{}
	in := &instance.Instance{Name: "friends"}
	d := &spec.Definition{Name: "Terraria", Stop: spec.Stop{TimeoutSeconds: 60}}
	u := s.Unit(in, d, Env{Exe: "/home/me/My Apps/anyserve", Home: "/srv/any%serve"})
	for _, want := range []string{
		`ExecStart="/home/me/My Apps/anyserve" run friends`,
		`Environment="ANYSERVE_HOME=/srv/any%%serve"`,
		"KillMode=mixed",
		"TimeoutStopSec=105",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "User=") {
		t.Error("user units must not set User=")
	}
}

func TestNativeSupport(t *testing.T) {
	d := &spec.Definition{Name: "X", Platforms: []string{"plan9"}}
	if err := nativeSupport(d); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("want a hint about containers, got %v", err)
	}
}
