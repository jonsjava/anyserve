//go:build !windows

package supervise

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// When run as a helper, the test binary pretends to be a game server: it
// echoes console input and exits on "exit".
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_SERVER") {
	case "obedient":
		fmt.Println("ready")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Println("got:", sc.Text())
			if sc.Text() == "exit" {
				fmt.Println("saving and exiting")
				os.Exit(0)
			}
		}
		os.Exit(0)
	case "stubborn":
		// Ignores stop commands and SIGINT; only SIGKILL works.
		ignoreInterrupt()
		fmt.Println("ready")
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func options(t *testing.T, mode string, out *safeBuf) Options {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Options{
		Command:     exe,
		Dir:         dir,
		Env:         append(os.Environ(), "FAKE_SERVER="+mode),
		StopCommand: "exit",
		StopTimeout: 2 * time.Second,
		Socket:      filepath.Join(dir, "c.sock"),
		LogDir:      filepath.Join(dir, "logs"),
		Stdout:      out,
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestConsoleAndCleanStop(t *testing.T) {
	out := &safeBuf{}
	o := options(t, "obedient", out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()

	waitFor(t, func() bool { _, err := os.Stat(o.Socket); return err == nil })
	if err := Send(o.Socket, "say hello"); err != nil {
		t.Fatal(err)
	}
	if err := Send(o.Socket, "two\nlines"); err == nil {
		t.Fatal("multi-line commands must be refused")
	}
	st, _ := os.Stat(o.Socket)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want 0600", st.Mode().Perm())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean stop returned %v", err)
	}
	got := out.String()
	for _, want := range []string{"got: say hello", "got: exit", "saving and exiting"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	log, _ := os.ReadFile(filepath.Join(o.LogDir, "server.log"))
	if !strings.Contains(string(log), "got: say hello") {
		t.Errorf("log file missing output: %q", log)
	}
	if _, err := os.Stat(o.Socket); !os.IsNotExist(err) {
		t.Error("socket should be removed after stop")
	}
}

func TestStubbornServerIsKilled(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ~20s")
	}
	out := &safeBuf{}
	o := options(t, "stubborn", out)
	o.StopTimeout = 500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	waitFor(t, func() bool { return strings.Contains(out.String(), "ready") })
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("want killed error, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stubborn server was never killed")
	}
}

func TestUnexpectedExitIsAnError(t *testing.T) {
	out := &safeBuf{}
	o := options(t, "obedient", out)
	o.Socket = ""
	// Close stdin immediately by running a server that exits on EOF.
	o.Env = append(o.Env, "FAKE_SERVER=obedient")
	o.Command = "/bin/sh"
	o.Args = []string{"-c", "echo crash; exit 3"}
	err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want exit status error, got %v", err)
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
