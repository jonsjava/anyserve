package target

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

func init() { register(&systemd{}) }

// systemd runs instances as systemd user services, so nothing needs root.
type systemd struct{}

func (s *systemd) Name() string { return "systemd" }
func (s *systemd) Description() string {
	return "Linux: a systemd user service (starts on boot once lingering is enabled)"
}

func (s *systemd) Available() error {
	if runtime.GOOS != "linux" {
		return errors.New("systemd is only available on Linux")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found; this system doesn't use systemd")
	}
	out, _ := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput()
	state := strings.TrimSpace(string(out))
	switch state {
	case "running", "degraded", "starting", "initializing", "maintenance":
		return nil
	}
	return fmt.Errorf("the systemd user manager isn't reachable (%s); are you logged in through a normal session?", state)
}

func (s *systemd) SupportsService(d *spec.Definition) error { return nativeSupport(d) }

func unitName(in *instance.Instance) string { return "anyserve-" + in.Name + ".service" }

func unitDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// Unit renders the unit file. Exported for tests.
func (s *systemd) Unit(in *instance.Instance, d *spec.Definition, env Env) string {
	stop := d.Stop.TimeoutSeconds
	if stop <= 0 {
		stop = 60
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Managed by Anyserve. Changes are overwritten; use `anyserve` to change this service.\n")
	fmt.Fprintf(&b, "[Unit]\nDescription=Anyserve: %s (%s)\nAfter=network-online.target\nWants=network-online.target\n\n", in.Name, d.Name)
	fmt.Fprintf(&b, "[Service]\nType=simple\n")
	if env.Home != "" {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote("ANYSERVE_HOME="+env.Home))
	}
	fmt.Fprintf(&b, "ExecStart=%s run %s\n", systemdQuote(env.Exe), in.Name)
	// Only the supervisor gets SIGTERM; it types the server's stop command
	// and waits. Anything left after the timeout is killed.
	fmt.Fprintf(&b, "KillMode=mixed\nTimeoutStopSec=%d\n", stop+45)
	fmt.Fprintf(&b, "Restart=on-failure\nRestartSec=15\n")
	fmt.Fprintf(&b, "NoNewPrivileges=yes\n\n")
	fmt.Fprintf(&b, "[Install]\nWantedBy=default.target\n")
	return b.String()
}

func (s *systemd) Register(in *instance.Instance, d *spec.Definition, env Env) error {
	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, unitName(in))
	if err := os.WriteFile(p, []byte(s.Unit(in, d, env)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unitName(in)); err != nil {
		return err
	}
	if out, _ := exec.Command("loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger", "--value").Output(); strings.TrimSpace(string(out)) != "yes" && env.Out != nil {
		fmt.Fprintf(env.Out, "Note: user services stop when you log out and don't start at boot until lingering is on:\n  sudo loginctl enable-linger %s\n", currentUser())
	}
	return nil
}

func (s *systemd) Unregister(in *instance.Instance) error {
	systemctl("disable", "--now", unitName(in))
	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, unitName(in))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return systemctl("daemon-reload")
}

func (s *systemd) Start(in *instance.Instance) error { return systemctl("start", unitName(in)) }
func (s *systemd) Stop(in *instance.Instance) error  { return systemctl("stop", unitName(in)) }

func (s *systemd) Status(in *instance.Instance) Status {
	out, _ := exec.Command("systemctl", "--user", "is-active", unitName(in)).Output()
	switch state := strings.TrimSpace(string(out)); state {
	case "active", "reloading":
		return Status{State: "running"}
	case "activating":
		return Status{State: "starting"}
	case "deactivating":
		return Status{State: "stopping"}
	case "failed":
		return Status{State: "failed", Detail: "see `anyserve logs " + in.Name + "`"}
	case "inactive":
		return Status{State: "stopped"}
	default:
		return Status{State: "unknown", Detail: state}
	}
}

func (s *systemd) Logs(in *instance.Instance, lines int, follow bool, out io.Writer) error {
	args := []string{"--user", "-u", unitName(in), "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// systemdQuote quotes a value for an ExecStart= or Environment= line.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$%") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "$USER"
}
