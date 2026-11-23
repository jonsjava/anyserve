package app

import (
	"testing"

	"github.com/jonsjava/anyserve/internal/spec"
)

func TestTargetFor(t *testing.T) {
	native := &spec.Definition{Name: "mc", Run: map[string]spec.Run{"linux": {Command: "java"}}, Ports: []spec.Port{
		{Name: "game", Port: 25565, Protocol: "tcp", Public: true, SRV: "_minecraft._tcp"},
		{Name: "rcon", Port: 25575, Protocol: "tcp"},
	}}
	if tg, err := TargetFor(native, ""); err != nil || tg.Kind != "" {
		t.Errorf("native without routing should be DNS only: %+v %v", tg, err)
	}
	native.Ports[0].Routing = spec.RouteMinecraft
	if tg, _ := TargetFor(native, ""); tg.Kind != spec.RouteMinecraft || tg.Port.Name != "game" {
		t.Errorf("routing port should win: %+v", tg)
	}
	if tg, _ := TargetFor(native, "rcon"); tg.Kind != "" || tg.Port.Name != "rcon" {
		t.Errorf("named port without routing: %+v", tg)
	}
	if _, err := TargetFor(native, "nope"); err == nil {
		t.Error("unknown port accepted")
	}
	stack := &spec.Definition{Name: "web", Kind: spec.KindStack, Ports: []spec.Port{{Name: "http", Port: 8080, Protocol: "tcp"}}}
	if tg, err := TargetFor(stack, ""); err != nil || tg.Kind != spec.RouteHTTP || tg.Port.Name != "http" {
		t.Errorf("stack web port: %+v %v", tg, err)
	}
}
