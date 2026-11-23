package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/paths"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/target"
)

func TestDefinitionChanged(t *testing.T) {
	a := &spec.Definition{ID: "x", Version: "1"}
	b := &spec.Definition{ID: "x", Version: "1"}
	if DefinitionChanged(a, b) {
		t.Fatal("identical definitions reported as changed")
	}
	b.Name = "other"
	if !DefinitionChanged(a, b) {
		t.Fatal("same version with different content should count as changed")
	}
}

func TestPolicyDefault(t *testing.T) {
	if p := Policy(&instance.Instance{}); p != PolicyNotify {
		t.Fatalf("default policy = %q", p)
	}
}

func TestWaitFor(t *testing.T) {
	ctx := context.Background()
	seq := func(states ...target.Status) func() target.Status {
		i := 0
		return func() target.Status {
			s := states[min(i, len(states)-1)]
			i++
			return s
		}
	}
	run := target.Status{State: "running"}
	starting := target.Status{State: "running", Detail: "web: running (starting)"}
	failed := target.Status{State: "failed"}

	if err := waitFor(ctx, seq(starting, starting, run), time.Second, 0, time.Millisecond); err != nil {
		t.Errorf("should become healthy: %v", err)
	}
	if err := waitFor(ctx, seq(failed), 20*time.Millisecond, 0, time.Millisecond); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Errorf("should time out as failed, got %v", err)
	}
	// Healthy for a moment, then crashing again, never settles.
	flap := func() func() target.Status {
		i := 0
		return func() target.Status {
			i++
			if i%3 == 0 {
				return failed
			}
			return run
		}
	}()
	if err := waitFor(ctx, flap, 50*time.Millisecond, 20*time.Millisecond, 2*time.Millisecond); err == nil {
		t.Error("a flapping server shouldn't count as healthy")
	}
}

func TestUpdateTimerUnits(t *testing.T) {
	svc, tmr := target.UpdateTimerUnits(target.Env{Exe: "/usr/bin/anyserve", Home: "/srv/any"}, "*-*-* 04:00")
	for _, want := range []string{"Type=oneshot", "ExecStart=/usr/bin/anyserve auto-update run", "ANYSERVE_HOME=/srv/any"} {
		if !strings.Contains(svc, want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=*-*-* 04:00", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(tmr, want) {
			t.Errorf("timer lacks %q:\n%s", want, tmr)
		}
	}
}

func TestNotify(t *testing.T) {
	var got, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, title = string(b), r.Header.Get("Title")
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := &App{Dirs: paths.Dirs{Config: dir, Data: dir, Cache: dir}, Log: func(string, ...any) {}}
	if err := a.SaveConfig(&Config{NotifyURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	a.notify([]UpdateResult{{Name: "quiet", Outcome: "up to date"}})
	if got != "" {
		t.Fatalf("nothing to report, but posted %q", got)
	}
	a.notify([]UpdateResult{
		{Name: "web", Outcome: "available", Available: "definition 1 → 2"},
		{Name: "game", Outcome: "rolled back", Available: "Steam build 1 → 2", Detail: "not healthy"},
	})
	want := "game: rolled back (Steam build 1 → 2) — not healthy\nweb: available (definition 1 → 2)"
	if got != want || !strings.HasPrefix(title, "Anyserve updates on ") {
		t.Fatalf("posted %q (title %q), want %q", got, title, want)
	}
}

func TestWebPort(t *testing.T) {
	d := &spec.Definition{Name: "x", Ports: []spec.Port{{Name: "game", Port: 1, Protocol: "udp", Public: true}, {Name: "admin", Port: 2, Protocol: "tcp"}}}
	if p, err := WebPort(d, ""); err != nil || p.Name != "admin" {
		t.Errorf("only private TCP port: %v %v", p, err)
	}
	d.Ports = append(d.Ports, spec.Port{Name: "http", Port: 3, Protocol: "tcp"})
	if p, _ := WebPort(d, ""); p.Name != "http" {
		t.Errorf("http should win: %v", p)
	}
	if _, err := WebPort(d, "game"); err == nil {
		t.Error("a UDP port was accepted")
	}
	d.Ports = []spec.Port{{Name: "a", Protocol: "tcp"}, {Name: "b", Protocol: "tcp"}}
	if _, err := WebPort(d, ""); err == nil {
		t.Error("ambiguous ports should need --domain-port")
	}
}
