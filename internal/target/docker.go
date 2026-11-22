package target

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/steps"
)

func init() { register(&docker{}) }

// docker runs stack definitions with Docker Compose. Anyserve writes the
// compose file itself from the definition, so a definition can only ask for
// what the stack schema allows.
type docker struct{}

func (t *docker) Name() string { return "docker" }
func (t *docker) Description() string {
	return "Any OS: Docker containers through Docker Compose (stacks only for now)"
}

func (t *docker) Available() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker isn't installed; see https://docs.docker.com/engine/install/")
	}
	if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose isn't available (%s); install the Docker Compose plugin", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("can't reach the Docker daemon: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (t *docker) SupportsService(d *spec.Definition) error {
	if !d.IsStack() {
		return fmt.Errorf("%s is a native server; running native servers in containers isn't built yet, so use the %s target", d.Name, Default())
	}
	return nil
}

func project(in *instance.Instance) string { return "anyserve-" + in.Name }

func composePath(in *instance.Instance) string { return filepath.Join(in.Dir, "compose.yaml") }

func (t *docker) compose(in *instance.Instance, args ...string) *exec.Cmd {
	all := append([]string{"compose", "-p", project(in), "-f", composePath(in)}, args...)
	cmd := exec.Command("docker", all...)
	cmd.Dir = in.Dir
	return cmd
}

func (t *docker) run(in *instance.Instance, out io.Writer, args ...string) error {
	cmd := t.compose(in, args...)
	var buf bytes.Buffer
	if out == nil {
		cmd.Stdout, cmd.Stderr = &buf, &buf
	} else {
		cmd.Stdout, cmd.Stderr = out, io.MultiWriter(out, &buf)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if len(msg) > 2000 {
			msg = "..." + msg[len(msg)-2000:]
		}
		return fmt.Errorf("docker compose %s: %w\n%s", args[0], err, msg)
	}
	return nil
}

// Register writes the compose file, creates the stack's folders and pulls
// the images.
func (t *docker) Register(in *instance.Instance, d *spec.Definition, env Env) error {
	body, err := Compose(in, d, os.Getuid(), os.Getgid())
	if err != nil {
		return err
	}
	if err := prepareVolumes(in, d); err != nil {
		return err
	}
	if err := os.WriteFile(composePath(in), body, 0o600); err != nil {
		return err
	}
	if err := t.run(in, nil, "config", "--quiet"); err != nil {
		return err
	}
	if env.Out != nil {
		fmt.Fprintln(env.Out, "Pulling container images (this can take a while)")
	}
	return t.run(in, nil, "pull", "--quiet")
}

// prepareVolumes creates folder volumes before Docker does, so they're
// owned by the user rather than root.
func prepareVolumes(in *instance.Instance, d *spec.Definition) error {
	for _, s := range d.Stack.Services {
		for _, v := range s.Volumes {
			if v.Setting != "" || d.IsFileVolume(v.Path) {
				continue
			}
			p, err := steps.Resolve(in.ServerDir, v.Path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *docker) Unregister(in *instance.Instance) error {
	if _, err := os.Stat(composePath(in)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return t.run(in, nil, "down", "--remove-orphans")
}

func (t *docker) Start(in *instance.Instance) error {
	return t.run(in, nil, "up", "--detach", "--remove-orphans")
}

// Stop stops every container, giving each the stop timeout from the
// definition (written into the compose file) before it's killed.
func (t *docker) Stop(in *instance.Instance) error {
	return t.run(in, nil, "stop")
}

type psEntry struct {
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

func (t *docker) Status(in *instance.Instance) Status {
	cmd := t.compose(in, "ps", "--all", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return Status{State: "unknown", Detail: "docker compose ps failed"}
	}
	var entries []psEntry
	trimmed := bytes.TrimSpace(out)
	if bytes.HasPrefix(trimmed, []byte("[")) {
		json.Unmarshal(trimmed, &entries)
	} else {
		sc := bufio.NewScanner(bytes.NewReader(trimmed))
		for sc.Scan() {
			var e psEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				entries = append(entries, e)
			}
		}
	}
	return summarize(entries)
}

// summarize turns per-container states into one status.
func summarize(entries []psEntry) Status {
	if len(entries) == 0 {
		return Status{State: "stopped"}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Service < entries[j].Service })
	running, failed := 0, []string{}
	var parts []string
	for _, e := range entries {
		s := e.State
		if e.Health != "" {
			s += " (" + e.Health + ")"
		}
		parts = append(parts, e.Service+": "+s)
		switch {
		case e.State == "running" && e.Health != "unhealthy":
			running++
		case e.State == "restarting" || e.Health == "unhealthy" || (e.State == "exited" && e.ExitCode != 0):
			failed = append(failed, e.Service)
		}
	}
	detail := strings.Join(parts, ", ")
	switch {
	case len(failed) > 0:
		return Status{State: "failed", Detail: detail}
	case running == len(entries):
		return Status{State: "running", Detail: detail}
	case running == 0:
		return Status{State: "stopped", Detail: detail}
	}
	return Status{State: "partly running", Detail: detail}
}

func (t *docker) Logs(in *instance.Instance, lines int, follow bool, out io.Writer) error {
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(lines)}
	if follow {
		args = append(args, "--follow")
	}
	cmd := t.compose(in, args...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// composeService is the subset of the Compose file format Anyserve writes.
type composeService struct {
	Image       string                       `yaml:"image"`
	Command     []string                     `yaml:"command,omitempty"`
	Environment map[string]string            `yaml:"environment,omitempty"`
	User        string                       `yaml:"user"`
	Restart     string                       `yaml:"restart"`
	CapDrop     []string                     `yaml:"cap_drop"`
	SecurityOpt []string                     `yaml:"security_opt"`
	Volumes     []composeVolume              `yaml:"volumes,omitempty"`
	Ports       []composePort                `yaml:"ports,omitempty"`
	DependsOn   map[string]map[string]string `yaml:"depends_on,omitempty"`
	Healthcheck *composeHealth               `yaml:"healthcheck,omitempty"`
	ShmSize     string                       `yaml:"shm_size,omitempty"`
	StopGrace   string                       `yaml:"stop_grace_period"`
	Logging     map[string]any               `yaml:"logging"`
}

type composeVolume struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

type composePort struct {
	Target    int    `yaml:"target"`
	Published string `yaml:"published"`
	HostIP    string `yaml:"host_ip"`
	Protocol  string `yaml:"protocol"`
}

type composeHealth struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval"`
	Retries  int      `yaml:"retries"`
}

// Compose renders the compose file for a stack instance. Exported for tests.
func Compose(in *instance.Instance, d *spec.Definition, uid, gid int) ([]byte, error) {
	if !d.IsStack() {
		return nil, fmt.Errorf("%s isn't a stack", d.Name)
	}
	ctx, err := in.Context(d, "linux")
	if err != nil {
		return nil, err
	}
	services, err := d.RenderStack(ctx)
	if err != nil {
		return nil, err
	}
	healthy := map[string]bool{}
	for _, s := range services {
		healthy[s.Name] = s.Health != nil
	}
	grace := d.Stop.TimeoutSeconds
	if grace <= 0 {
		grace = 30
	}
	out := map[string]composeService{}
	for _, s := range services {
		cs := composeService{
			Image:       s.Image,
			Command:     s.Command,
			Environment: s.Env,
			// Containers run as the user who owns the instance, so their
			// files stay readable, backed up and removable without root.
			User:        fmt.Sprintf("%d:%d", uid, gid),
			Restart:     "unless-stopped",
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges:true"},
			StopGrace:   fmt.Sprintf("%ds", grace),
			Logging: map[string]any{
				"driver":  "json-file",
				"options": map[string]string{"max-size": "10m", "max-file": "3"},
			},
		}
		if len(cs.Environment) == 0 {
			cs.Environment = nil
		}
		for _, v := range s.Volumes {
			if v.Setting != "" {
				// A host folder the user chose. Checked again here in case
				// it changed (or became a symlink) since install.
				chosen, _ := ctx.Settings[v.Setting].(string)
				if chosen == "" {
					continue
				}
				src, err := spec.HostFolder(chosen)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", v.Setting, err)
				}
				cs.Volumes = append(cs.Volumes, composeVolume{Type: "bind", Source: src, Target: v.Target, ReadOnly: v.ReadOnly})
				continue
			}
			src, err := steps.Resolve(in.ServerDir, v.Path)
			if err != nil {
				return nil, err
			}
			cs.Volumes = append(cs.Volumes, composeVolume{Type: "bind", Source: src, Target: v.Target, ReadOnly: v.ReadOnly})
		}
		for _, p := range s.Ports {
			def, _ := d.Port(p.Port)
			host := "0.0.0.0"
			if !def.Public && !in.Exposed(p.Port) {
				host = "127.0.0.1"
			}
			for _, proto := range protocols(def.Protocol) {
				cs.Ports = append(cs.Ports, composePort{Target: p.Target, Published: strconv.Itoa(ctx.Ports[p.Port]), HostIP: host, Protocol: proto})
			}
		}
		if len(s.DependsOn) > 0 {
			cs.DependsOn = map[string]map[string]string{}
			for _, dep := range s.DependsOn {
				cond := "service_started"
				if healthy[dep] {
					cond = "service_healthy"
				}
				cs.DependsOn[dep] = map[string]string{"condition": cond}
			}
		}
		if h := s.Health; h != nil {
			interval := h.IntervalSeconds
			if interval == 0 {
				interval = 10
			}
			retries := h.Retries
			if retries == 0 {
				retries = 10
			}
			cs.Healthcheck = &composeHealth{Test: append([]string{"CMD"}, h.Command...), Interval: fmt.Sprintf("%ds", interval), Retries: retries}
		}
		if s.ShmMB > 0 {
			cs.ShmSize = fmt.Sprintf("%dm", s.ShmMB)
		}
		out[s.Name] = cs
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Written by Anyserve for instance %q (%s). Changes are overwritten.\n", in.Name, d.Name)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{"name": project(in), "services": out}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func protocols(p string) []string {
	if p == "both" {
		return []string{"tcp", "udp"}
	}
	return []string{p}
}

// DockerExec runs a program inside one of a stack's running containers.
// With tty set it attaches the terminal, for interactive shells.
func DockerExec(in *instance.Instance, service string, command []string, tty bool, stdin io.Reader, stdout, stderr io.Writer) error {
	args := []string{"exec"}
	if tty {
		args = append(args, "--interactive", "--tty")
	} else {
		args = append(args, "--no-TTY")
	}
	args = append(args, service)
	args = append(args, command...)
	cmd := (&docker{}).compose(in, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}
