package spec

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"text/template"
)

// Context is the data available to every template in a definition.
type Context struct {
	// Name is the instance name, e.g. "my-rust".
	Name string
	// Dir is the absolute instance directory, in the host's path format.
	Dir string
	// OS is the host OS (linux, windows, darwin).
	OS       string
	Settings map[string]any
	Ports    map[string]int
	// Message is set only when rendering console.say.
	Message string
}

var funcs = template.FuncMap{
	"lower": strings.ToLower,
	// json renders a value as a JSON literal (a quoted, escaped string for
	// text), for JSON, HJSON and YAML config files.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"upper": strings.ToUpper,
	// yesno renders a bool as "yes"/"no"; truefalse as "true"/"false";
	// onezero as "1"/"0". Servers disagree on which they want.
	"yesno": func(v any) string {
		if truthy(v) {
			return "yes"
		}
		return "no"
	},
	"truefalse": func(v any) string { return strconv.FormatBool(truthy(v)) },
	"onezero": func(v any) string {
		if truthy(v) {
			return "1"
		}
		return "0"
	},
}

// Render executes one template string against ctx.
func Render(name, tmpl string, ctx Context) (string, error) {
	t, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, ctx); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return b.String(), nil
}

// RenderRun renders the run entry for one OS.
func (d *Definition) RenderRun(goos string, ctx Context) (Run, error) {
	r, ok := d.Run[goos]
	if !ok {
		return Run{}, fmt.Errorf("%s has no native server for %s", d.Name, goos)
	}
	out := Run{Command: r.Command, Via: r.Via, Workdir: r.Workdir, Env: map[string]string{}}
	for i, a := range r.Args {
		s, err := Render(fmt.Sprintf("run.%s.args[%d]", goos, i), a, ctx)
		if err != nil {
			return Run{}, err
		}
		// An argument that renders to nothing is dropped, so optional
		// flags can be written as {{if .Settings.x}}-flag{{end}}.
		if s == "" {
			continue
		}
		out.Args = append(out.Args, s)
	}
	for k, v := range r.Env {
		s, err := Render(fmt.Sprintf("run.%s.env[%s]", goos, k), v, ctx)
		if err != nil {
			return Run{}, err
		}
		out.Env[k] = s
	}
	return out, nil
}

// ResolveSettings merges user-supplied raw string values with defaults,
// converts them to their declared types and generates secrets. It returns
// typed values ready for templates.
func (d *Definition) ResolveSettings(raw map[string]string) (map[string]any, error) {
	for k := range raw {
		if _, ok := d.Setting(k); !ok {
			return nil, fmt.Errorf("%s has no setting %q (available: %s)", d.Name, k, strings.Join(d.settingKeys(), ", "))
		}
	}
	out := map[string]any{}
	for _, s := range d.Settings {
		v, given := raw[s.Key]
		if !given {
			v = s.DefaultString()
		}
		if v == "" && s.Generate {
			v = RandomPassword(20)
		}
		typed, err := s.Convert(v)
		if err != nil {
			return nil, err
		}
		out[s.Key] = typed
	}
	return out, nil
}

// Convert parses a raw string into the setting's type and checks limits.
func (s Setting) Convert(v string) (any, error) {
	switch s.Type {
	case "int":
		if v == "" {
			if s.Optional {
				return 0, nil
			}
			return nil, fmt.Errorf("%s is required", s.Key)
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%s must be a whole number (got %q)", s.Key, v)
		}
		if s.Min != nil && n < *s.Min {
			return nil, fmt.Errorf("%s must be at least %d", s.Key, *s.Min)
		}
		if s.Max != nil && n > *s.Max {
			return nil, fmt.Errorf("%s must be at most %d", s.Key, *s.Max)
		}
		return n, nil
	case "bool":
		if v == "" {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "y", "1", "on":
			return true, nil
		case "false", "no", "n", "0", "off":
			return false, nil
		}
		return nil, fmt.Errorf("%s must be true or false (got %q)", s.Key, v)
	case "choice":
		for _, c := range s.Choices {
			if c.Value == v {
				return v, nil
			}
		}
		vals := make([]string, len(s.Choices))
		for i, c := range s.Choices {
			vals[i] = c.Value
		}
		return nil, fmt.Errorf("%s must be one of %s (got %q)", s.Key, strings.Join(vals, ", "), v)
	default:
		if v == "" && !s.Optional {
			return nil, fmt.Errorf("%s is required", s.Key)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("%s must be a single line", s.Key)
		}
		return v, nil
	}
}

func (d *Definition) settingKeys() []string {
	out := make([]string, len(d.Settings))
	for i, s := range d.Settings {
		out[i] = s.Key
	}
	return out
}

// ResolvePorts merges user overrides with the definition's default ports.
func (d *Definition) ResolvePorts(overrides map[string]int) (map[string]int, error) {
	for k := range overrides {
		if _, ok := d.Port(k); !ok {
			names := make([]string, len(d.Ports))
			for i, p := range d.Ports {
				names[i] = p.Name
			}
			return nil, fmt.Errorf("%s has no port named %q (available: %s)", d.Name, k, strings.Join(names, ", "))
		}
	}
	out := map[string]int{}
	for _, p := range d.Ports {
		n := p.Port
		if o, ok := overrides[p.Name]; ok {
			if o < 1 || o > 65535 {
				return nil, fmt.Errorf("port %s must be 1-65535", p.Name)
			}
			n = o
		}
		out[p.Name] = n
	}
	return out, nil
}

// RandomPassword returns a random password made of unambiguous characters.
func RandomPassword(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int:
		return t != 0
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	}
	return false
}
