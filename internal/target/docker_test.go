package target

import (
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

const img = "example/app:1@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func stackDef() *spec.Definition {
	return &spec.Definition{
		Schema: 1, ID: "demo", Name: "Demo", Description: "d", Version: "1", Kind: spec.KindStack,
		Settings: []spec.Setting{{Key: "pw", Prompt: "pw", Type: "string", Default: "s3cret"}},
		Ports: []spec.Port{
			{Name: "web", Port: 8080, Protocol: "tcp", Public: true},
			{Name: "admin", Port: 9090, Protocol: "both"},
		},
		Files: []spec.File{{Path: "conf/app.conf", Template: "x"}},
		Stack: &spec.Stack{Services: []spec.StackService{
			{Name: "db", Image: img, Env: map[string]string{"PASSWORD": "{{.Settings.pw}}"},
				Volumes: []spec.Volume{{Path: "data/db", Target: "/var/lib/db"}},
				Health:  &spec.HealthCheck{Command: []string{"check"}}},
			{Name: "app", Image: img, DependsOn: []string{"db"},
				Volumes: []spec.Volume{{Path: "conf/app.conf", Target: "/etc/app.conf", ReadOnly: true}},
				Ports:   []spec.ServicePort{{Port: "web", Target: 80}, {Port: "admin", Target: 90}}},
		}},
	}
}

func TestComposeIsLockedDown(t *testing.T) {
	d := stackDef()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := &instance.Instance{Name: "x", Dir: dir, ServerDir: dir, Ports: map[string]int{"web": 8081}}
	b, err := Compose(in, d, 1001, 1002)
	if err != nil {
		t.Fatal(err)
	}
	c := string(b)
	for _, want := range []string{
		"name: anyserve-x",
		"user: 1001:1002",
		"- ALL",
		"no-new-privileges:true",
		"PASSWORD: s3cret",
		"source: " + dir + "/data/db",
		"source: " + dir + "/conf/app.conf",
		"read_only: true",
		`published: "8081"`,
		"host_ip: 0.0.0.0",
		"host_ip: 127.0.0.1", // non-public ports stay local
		"protocol: udp",
		"condition: service_healthy",
		"- CMD",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("compose missing %q:\n%s", want, c)
		}
	}
	for _, bad := range []string{"privileged", "network_mode", "docker.sock", "cap_add"} {
		if strings.Contains(c, bad) {
			t.Errorf("compose must not contain %q", bad)
		}
	}
}

func TestStackValidation(t *testing.T) {
	d := stackDef()
	d.Stack.Services[0].Image = "example/app:latest"
	d.Stack.Services[1].DependsOn = []string{"nope"}
	d.Stack.Services[1].Volumes[0].Target = "relative"
	d.Platforms = []string{"linux"}
	err := d.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"pinned by digest", `"nope" isn't another service`, "clean absolute path", "leave platforms out"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct {
		in   []psEntry
		want string
	}{
		{nil, "stopped"},
		{[]psEntry{{Service: "a", State: "running"}, {Service: "b", State: "running", Health: "healthy"}}, "running"},
		{[]psEntry{{Service: "a", State: "running"}, {Service: "b", State: "exited"}}, "partly running"},
		{[]psEntry{{Service: "a", State: "running"}, {Service: "b", State: "exited", ExitCode: 1}}, "failed"},
		{[]psEntry{{Service: "a", State: "exited"}}, "stopped"},
	}
	for _, c := range cases {
		if got := summarize(c.in).State; got != c.want {
			t.Errorf("%v: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestTargetsRefuseTheWrongKind(t *testing.T) {
	if err := (&docker{}).SupportsService(&spec.Definition{Name: "T"}); err == nil {
		t.Error("docker should refuse native servers for now")
	}
	if err := nativeSupport(stackDef()); err == nil {
		t.Error("native targets should refuse stacks")
	}
}
