package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/spec"
)

func TestRuntimeCheck(t *testing.T) {
	d, err := spec.LoadFile(filepath.Join("..", "..", "registry", "services", "minecraft.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	noJava := func(p string) (string, error) { return "", errors.New("not found") }
	hasJava := func(p string) (string, error) { return "/usr/bin/" + p, nil }
	ok := func() error { return nil }
	noDocker := func() error { return errors.New("docker isn't installed") }

	if iso, notes := runtimeCheck(d, "mc", "linux", hasJava, ok); iso || len(notes) > 0 {
		t.Errorf("java installed: %v %v", iso, notes)
	}
	iso, notes := runtimeCheck(d, "mc", "linux", noJava, ok)
	if !iso || len(notes) != 1 || !strings.Contains(notes[0], "eclipse-temurin") || !strings.Contains(notes[0], "anyserve config mc --no-isolate") {
		t.Errorf("no java, docker available: %v %q", iso, notes)
	}
	iso, notes = runtimeCheck(d, "mc", "linux", noJava, noDocker)
	if iso || len(notes) != 1 || !strings.Contains(notes[0], "won't start") {
		t.Errorf("no java, no docker: %v %q", iso, notes)
	}
}
