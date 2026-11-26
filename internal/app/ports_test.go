package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/spec"
)

func TestFormatPortsExposed(t *testing.T) {
	d, err := spec.LoadFile(filepath.Join("..", "..", "registry", "services", "neko.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{"http": 8085, "webrtc": 52000}
	line := func(out, name string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), name+" ") {
				return l
			}
		}
		t.Fatalf("no %s line in:\n%s", name, out)
		return ""
	}
	out := FormatPorts(d, ports)
	if !strings.Contains(line(out, "http"), "keep private") {
		t.Errorf("private port: %q", line(out, "http"))
	}
	if !strings.Contains(line(out, "webrtc"), "forward on router") {
		t.Errorf("public port: %q", line(out, "webrtc"))
	}
	if strings.Contains(out, "players") {
		t.Errorf("game wording for an app:\n%s", out)
	}
	if !strings.Contains(out, "sudo ufw allow 52000/tcp && sudo ufw allow 52000/udp") {
		t.Errorf("firewall hint:\n%s", out)
	}
	out = FormatPortsShared(d, ports, nil, []string{"http"})
	if !strings.Contains(line(out, "http"), "open to network") {
		t.Errorf("exposed port: %q", line(out, "http"))
	}
}
