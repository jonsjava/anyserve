package spec

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"text/template"
)

// Definition kinds.
const (
	// KindServer is one program Anyserve installs and supervises itself.
	KindServer = "server"
	// KindStack is a group of container images that run together, such as a
	// web app with its database and proxy. Only container targets run them.
	KindStack = "stack"
)

// Stack lists the containers in a stack definition.
//
// Stacks stay declarative like everything else: images must be pinned by
// digest, containers always run as the invoking user with every Linux
// capability dropped, and they can only mount folders and files inside the
// instance directory. There's no way to ask for privileged mode, the host
// network, host paths or the Docker socket.
type Stack struct {
	Services []StackService `yaml:"services" json:"services"`
}

// StackService is one container in a stack.
type StackService struct {
	// Name is the container's name in the stack, and its host name on the
	// stack's private network.
	Name string `yaml:"name" json:"name"`
	// Image must be pinned by digest: name:tag@sha256:<64 hex>.
	Image string `yaml:"image" json:"image"`
	// Command replaces the image's command. Each item is a template.
	Command []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Volumes []Volume          `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Ports   []ServicePort     `yaml:"ports,omitempty" json:"ports,omitempty"`
	// DependsOn starts these services first, waiting until they're healthy
	// if they have a health check.
	DependsOn []string     `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Health    *HealthCheck `yaml:"health,omitempty" json:"health,omitempty"`
	// ShmMB sets the shared memory size, which databases often need raised.
	ShmMB int `yaml:"shm_mb,omitempty" json:"shm_mb,omitempty"`
	// Once marks a job that runs to completion on every start, such as
	// database migrations, instead of a long-running service. Services
	// that depend on it wait until it has finished successfully.
	Once bool `yaml:"once,omitempty" json:"once,omitempty"`
}

// Volume mounts a folder or file from the instance's server directory.
// Paths listed in the definition's files are mounted as files; anything
// else is a folder, created before the stack starts.
type Volume struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Setting mounts the host folder the user chose in a folder setting
	// (a media library, say) instead of a path inside the instance. If the
	// setting is optional and left empty, the mount is skipped.
	Setting  string `yaml:"setting,omitempty" json:"setting,omitempty"`
	Target   string `yaml:"target" json:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

// ServicePort publishes one of the definition's ports from a container.
type ServicePort struct {
	// Port names an entry in the definition's ports.
	Port string `yaml:"port" json:"port"`
	// Target is the port inside the container.
	Target int `yaml:"target" json:"target"`
}

// HealthCheck runs a command inside the container. Exit status 0 means
// healthy.
type HealthCheck struct {
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// Disable turns off a health check built into the image, for a
	// container that doesn't do what the image's check expects (a worker
	// started from a server's image, say).
	Disable         bool `yaml:"disable,omitempty" json:"disable,omitempty"`
	IntervalSeconds int  `yaml:"interval_seconds,omitempty" json:"interval_seconds,omitempty"`
	Retries         int  `yaml:"retries,omitempty" json:"retries,omitempty"`
}

// IsStack reports whether the definition is a container stack.
func (d *Definition) IsStack() bool { return d.Kind == KindStack }

var imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$`)

// validateStack checks the stack part of a definition.
func (d *Definition) validateStack(add func(string, ...any)) {
	if d.Stack == nil || len(d.Stack.Services) == 0 {
		add("stack.services: list at least one container")
		return
	}
	if len(d.Steps) > 0 {
		add("steps: not used by stacks")
	}
	if len(d.Run) > 0 {
		add("run: not used by stacks; describe the containers under stack.services")
	}
	if d.Console.Say != "" || d.Console.Save != "" || d.Stop.Command != "" {
		add("console and stop.command: stacks have no server console")
	}
	names := map[string]bool{}
	for _, s := range d.Stack.Services {
		names[s.Name] = true
	}
	usedPorts := map[string]bool{}
	seen := map[string]bool{}
	for i, s := range d.Stack.Services {
		where := fmt.Sprintf("stack.services[%d] (%s)", i, s.Name)
		if !idRe.MatchString(s.Name) {
			add("%s: name must be lowercase letters, digits and dashes", where)
		}
		if seen[s.Name] {
			add("%s: duplicate name", where)
		}
		seen[s.Name] = true
		if !imageRe.MatchString(s.Image) {
			add("%s: image must be pinned by digest, like name:tag@sha256:<64 hex characters>", where)
		}
		for j, c := range s.Command {
			if _, err := template.New("c").Funcs(funcs).Parse(c); err != nil {
				add("%s.command[%d]: template: %v", where, j, err)
			}
		}
		for k, v := range s.Env {
			if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k) {
				add("%s.env: %q isn't a valid variable name", where, k)
			}
			if _, err := template.New("e").Funcs(funcs).Parse(v); err != nil {
				add("%s.env[%s]: template: %v", where, k, err)
			}
		}
		for j, v := range s.Volumes {
			vw := fmt.Sprintf("%s.volumes[%d]", where, j)
			switch {
			case v.Path != "" && v.Setting != "":
				add("%s: set path or setting, not both", vw)
			case v.Setting != "":
				if st, ok := d.Setting(v.Setting); !ok || st.Type != "folder" {
					add("%s: setting %q must name a folder setting", vw, v.Setting)
				}
			default:
				if err := checkRelPath(v.Path); err != nil {
					add("%s: path: %v", vw, err)
				}
			}
			if !path.IsAbs(v.Target) || path.Clean(v.Target) != v.Target || v.Target == "/" {
				add("%s: target must be a clean absolute path inside the container", vw)
			}
		}
		for j, p := range s.Ports {
			pw := fmt.Sprintf("%s.ports[%d]", where, j)
			if _, ok := d.Port(p.Port); !ok {
				add("%s: no port named %q in ports", pw, p.Port)
			}
			if usedPorts[p.Port] {
				add("%s: port %q is published twice", pw, p.Port)
			}
			usedPorts[p.Port] = true
			if p.Target < 1 || p.Target > 65535 {
				add("%s: target must be 1-65535", pw)
			}
		}
		for _, dep := range s.DependsOn {
			if dep == s.Name || !names[dep] {
				add("%s.depends_on: %q isn't another service in this stack", where, dep)
			}
		}
		if h := s.Health; h != nil {
			switch {
			case h.Disable && len(h.Command) > 0:
				add("%s.health: set command or disable, not both", where)
			case !h.Disable && len(h.Command) == 0:
				add("%s.health.command is required", where)
			}
			for j, c := range h.Command {
				if _, err := template.New("h").Funcs(funcs).Parse(c); err != nil {
					add("%s.health.command[%d]: template: %v", where, j, err)
				}
			}
			if h.IntervalSeconds < 0 || h.Retries < 0 {
				add("%s.health: interval_seconds and retries must not be negative", where)
			}
		}
		if s.Once && (len(s.Ports) > 0 || (s.Health != nil && !s.Health.Disable)) {
			add("%s: a once job can't publish ports or have a health check", where)
		}
		if s.ShmMB < 0 {
			add("%s.shm_mb must not be negative", where)
		}
	}
	for _, p := range d.Ports {
		if !usedPorts[p.Name] {
			add("ports: %q isn't published by any stack service", p.Name)
		}
	}
}

// IsFileVolume reports whether a volume path is one of the definition's
// files or downloads, so it's mounted as a single file rather than a folder.
func (d *Definition) IsFileVolume(p string) bool {
	for _, f := range d.Files {
		if path.Clean(f.Path) == path.Clean(p) {
			return true
		}
	}
	for _, dl := range d.Install.Downloads {
		if path.Clean(dl.To) == path.Clean(p) {
			return true
		}
	}
	return false
}

// RenderedService is a stack service with its templates filled in.
type RenderedService struct {
	StackService
}

// RenderStack fills in every template in the stack.
func (d *Definition) RenderStack(ctx Context) ([]RenderedService, error) {
	var out []RenderedService
	for _, s := range d.Stack.Services {
		r := RenderedService{StackService: s}
		r.Command = nil
		for i, c := range s.Command {
			v, err := Render(fmt.Sprintf("stack.%s.command[%d]", s.Name, i), c, ctx)
			if err != nil {
				return nil, err
			}
			if v != "" {
				r.Command = append(r.Command, v)
			}
		}
		r.Env = map[string]string{}
		for k, v := range s.Env {
			rv, err := Render(fmt.Sprintf("stack.%s.env[%s]", s.Name, k), v, ctx)
			if err != nil {
				return nil, err
			}
			if strings.ContainsAny(rv, "\x00") {
				return nil, fmt.Errorf("stack.%s.env[%s]: contains a NUL byte", s.Name, k)
			}
			r.Env[k] = rv
		}
		if s.Health != nil {
			h := *s.Health
			h.Command = nil
			for i, c := range s.Health.Command {
				v, err := Render(fmt.Sprintf("stack.%s.health[%d]", s.Name, i), c, ctx)
				if err != nil {
					return nil, err
				}
				h.Command = append(h.Command, v)
			}
			r.Health = &h
		}
		out = append(out, r)
	}
	return out, nil
}
