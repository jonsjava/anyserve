// Package target turns an installed instance into a running service on a
// particular platform: a systemd unit, a Windows service, a launchd agent, a
// container or a Kubernetes manifest.
//
// Every native target runs the same thing: `anyserve run <name>`, which
// supervises the server. Targets only decide how that command gets started,
// restarted and logged.
package target

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// Status is a target's view of whether an instance is running.
type Status struct {
	State  string // running, stopped, failed, unknown
	Detail string
}

// Env is what a target needs from the caller.
type Env struct {
	// Exe is the absolute path of the anyserve binary.
	Exe string
	// Home is set when ANYSERVE_HOME is in use, so services see it too.
	Home string
	Out  io.Writer
}

// Target manages instances on one kind of platform.
type Target interface {
	Name() string
	Description() string
	// Available reports whether the target works on this machine, and why
	// not if it doesn't.
	Available() error
	// SupportsService reports whether a service can run on this target.
	SupportsService(d *spec.Definition) error
	// Register creates or refreshes the service for an instance.
	Register(in *instance.Instance, d *spec.Definition, env Env) error
	Unregister(in *instance.Instance) error
	Start(in *instance.Instance) error
	Stop(in *instance.Instance) error
	Status(in *instance.Instance) Status
	// Logs shows recent output, following it if follow is set.
	Logs(in *instance.Instance, lines int, follow bool, out io.Writer) error
}

var registry = map[string]Target{}

func register(t Target) { registry[t.Name()] = t }

// Get returns a target by name.
func Get(name string) (Target, error) {
	t, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown target %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	return t, nil
}

// Names lists every target, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All returns every target, sorted by name.
func All() []Target {
	var out []Target
	for _, n := range Names() {
		out = append(out, registry[n])
	}
	return out
}

// Default is the native service manager for this OS.
func Default() string {
	switch runtime.GOOS {
	case "windows":
		return "windows-service"
	case "darwin":
		return "launchd"
	default:
		return "systemd"
	}
}

// nativeSupport is shared by targets that run the server directly on the
// host OS.
func nativeSupport(d *spec.Definition) error {
	if d.IsStack() {
		return fmt.Errorf("%s is a container stack; install it with --target docker", d.Name)
	}
	if !d.SupportsOS(runtime.GOOS) {
		return fmt.Errorf("%s has no native server for %s (it supports %s); try the docker target", d.Name, osName(runtime.GOOS), strings.Join(osNames(d.Platforms), ", "))
	}
	return nil
}

func osName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return goos
}

func osNames(list []string) []string {
	out := make([]string, len(list))
	for i, o := range list {
		out[i] = osName(o)
	}
	return out
}
