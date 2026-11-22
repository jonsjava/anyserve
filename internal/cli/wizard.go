package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/registry"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/target"
)

func confirm(title string) (bool, error) {
	ok := false
	err := huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&ok).Run()
	return ok, err
}

// runWizard walks the user through an install: pick a service, a target, a
// name, settings and ports, review, install. It ends by printing the
// equivalent command so the same install can be scripted.
func runWizard(ctx context.Context, a *app.App) error {
	out := os.Stdout
	entries, warnings, err := a.Catalog()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "Warning:", w)
	}
	if len(entries) == 0 {
		return errors.New("no services available; try `anyserve refresh`")
	}

	// 1. Service.
	var ref string
	var opts []huh.Option[string]
	for _, e := range entries {
		id := e.Def.ID
		if e.Registry != registry.MainName {
			id = e.Registry + "/" + id
		}
		label := fmt.Sprintf("%s: %s", e.Def.Name, e.Def.Description)
		switch {
		case e.Def.IsStack():
			label += " (needs Docker)"
		case !e.Def.SupportsOS(runtime.GOOS):
			label += " (container only on this OS)"
		}
		opts = append(opts, huh.NewOption(label, id))
	}
	if err := huh.NewSelect[string]().Title("What do you want to host?").Options(opts...).Value(&ref).Run(); err != nil {
		return cancelled(err)
	}
	e, err := a.Find(ref)
	if err != nil {
		return err
	}
	d := e.Def
	for _, n := range d.Notes {
		fmt.Fprintf(out, "Note: %s\n", n)
	}

	// 2. Target.
	var targetName string
	var topts []huh.Option[string]
	var unavailable []string
	for _, t := range target.All() {
		if err := t.Available(); err != nil {
			unavailable = append(unavailable, fmt.Sprintf("  %s: %v", t.Name(), err))
			continue
		}
		if err := t.SupportsService(d); err != nil {
			unavailable = append(unavailable, fmt.Sprintf("  %s: %v", t.Name(), err))
			continue
		}
		label := t.Description()
		if t.Name() == target.Default() {
			label += " (recommended)"
		}
		topts = append(topts, huh.NewOption(label, t.Name()))
	}
	if len(topts) == 0 {
		return fmt.Errorf("no target on this machine can run %s yet:\n%s", d.Name, strings.Join(unavailable, "\n"))
	}
	targetName = topts[0].Value
	if len(topts) > 1 {
		if err := huh.NewSelect[string]().Title("Where should it run?").Options(topts...).Value(&targetName).Run(); err != nil {
			return cancelled(err)
		}
	}

	// 3. Name.
	name := d.ID
	for i := 2; a.Store.Exists(name); i++ {
		name = fmt.Sprintf("%s-%d", d.ID, i)
	}
	nameField := huh.NewInput().Title("Instance name").
		Description("Used for the service, folders and commands, e.g. `anyserve start " + name + "`.").
		Value(&name).
		Validate(func(s string) error {
			if err := instance.ValidateName(s); err != nil {
				return err
			}
			if a.Store.Exists(s) {
				return fmt.Errorf("%q is already taken", s)
			}
			return nil
		})

	// 4. Settings, one field each.
	raw := map[string]*string{}
	bools := map[string]*bool{}
	fields := []huh.Field{nameField}
	for _, s := range d.Settings {
		s := s
		v := s.DefaultString()
		raw[s.Key] = &v
		title := s.Prompt
		switch s.Type {
		case "bool":
			b := v == "true"
			bools[s.Key] = &b
			fields = append(fields, huh.NewConfirm().Title(title).Description(s.Description).Value(&b))
		case "choice":
			var copts []huh.Option[string]
			for _, c := range s.Choices {
				copts = append(copts, huh.NewOption(c.Label, c.Value))
			}
			fields = append(fields, huh.NewSelect[string]().Title(title).Description(s.Description).Options(copts...).Value(raw[s.Key]))
		default:
			in := huh.NewInput().Title(title).Description(s.Description).Value(raw[s.Key]).
				Validate(func(val string) error {
					if val == "" && s.Generate {
						return nil
					}
					_, err := s.Convert(val)
					return err
				})
			if s.Secret {
				in = in.EchoMode(huh.EchoModePassword)
			}
			if s.Generate {
				in = in.Placeholder("leave empty to generate a strong password")
			}
			fields = append(fields, in)
		}
	}
	if err := huh.NewForm(huh.NewGroup(fields...).Title("Settings for " + d.Name)).Run(); err != nil {
		return cancelled(err)
	}
	settings := map[string]string{}
	for k, v := range raw {
		settings[k] = *v
	}
	for k, b := range bools {
		settings[k] = strconv.FormatBool(*b)
	}

	// 5. Ports.
	defaults, _ := d.ResolvePorts(nil)
	fmt.Fprintln(out, "Default ports:")
	fmt.Fprint(out, app.FormatPorts(d, defaults))
	changePorts := false
	if err := huh.NewConfirm().Title("Change any ports?").Description("Only needed if another server already uses them.").Value(&changePorts).Run(); err != nil {
		return cancelled(err)
	}
	ports := map[string]int{}
	if changePorts {
		pvals := map[string]*string{}
		var pfields []huh.Field
		for _, p := range d.Ports {
			v := strconv.Itoa(p.Port)
			pvals[p.Name] = &v
			pfields = append(pfields, huh.NewInput().Title(fmt.Sprintf("%s port (%s)", p.Name, p.Protocol)).Description(p.Description).Value(&v).
				Validate(func(s string) error {
					n, err := strconv.Atoi(s)
					if err != nil || n < 1 || n > 65535 {
						return errors.New("enter a number from 1 to 65535")
					}
					return nil
				}))
		}
		if err := huh.NewForm(huh.NewGroup(pfields...)).Run(); err != nil {
			return cancelled(err)
		}
		for k, v := range pvals {
			n, _ := strconv.Atoi(*v)
			if n != defaults[k] {
				ports[k] = n
			}
		}
	}

	// 6. Review.
	req := app.InstallRequest{Service: ref, Name: name, Target: targetName, Settings: settings, Ports: ports}
	plan, err := a.PlanInstall(req)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Service:  %s\nName:     %s\nTarget:   %s\n", d.Name, name, targetName)
	for _, s := range d.Settings {
		shown := displayValue(s, plan.Settings[s.Key])
		if s.Generate && settings[s.Key] == "" {
			shown = "(generated; see `anyserve config " + name + " --show-secrets`)"
		}
		fmt.Fprintf(out, "  %s = %s\n", s.Key, shown)
	}
	for _, w := range plan.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", w)
	}
	start := true
	ok := true
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Start the server when the install finishes?").Value(&start),
		huh.NewConfirm().Title("Install now?").Value(&ok),
	)).Run(); err != nil {
		return cancelled(err)
	}
	if !ok {
		return errors.New("cancelled")
	}
	plan.Req.Start = start

	fmt.Fprintf(out, "\nThe same install as a command:\n  %s\n\n", equivalentCommand(d, req, start))

	if _, err := a.Install(ctx, plan); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nUseful next commands:\n  anyserve status %s\n  anyserve logs %s -f\n  anyserve ports %s\n", name, name, name)
	return nil
}

// equivalentCommand renders the scripted version of a wizard install.
// Secret values are left out so they don't end up in shell history.
func equivalentCommand(d *spec.Definition, req app.InstallRequest, start bool) string {
	parts := []string{"anyserve", "install", req.Service, "--name", req.Name}
	if req.Target != target.Default() {
		parts = append(parts, "--target", req.Target)
	}
	hidden := false
	for _, s := range d.Settings {
		v := req.Settings[s.Key]
		if v == s.DefaultString() || v == "" {
			continue
		}
		if s.Secret {
			hidden = true
			continue
		}
		parts = append(parts, "--set", shellQuote(s.Key+"="+v))
	}
	for _, p := range d.Ports {
		if n, ok := req.Ports[p.Name]; ok {
			parts = append(parts, "--port", fmt.Sprintf("%s=%d", p.Name, n))
		}
	}
	if start {
		parts = append(parts, "--start")
	}
	cmd := strings.Join(parts, " ")
	if hidden {
		cmd += "\n  (passwords left out; add them with --set key=value)"
	}
	return cmd
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t'\"$`\\!&;|<>()*?[]{}#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func cancelled(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errors.New("cancelled")
	}
	return err
}
