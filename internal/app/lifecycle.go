package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonsjava/anyserve/internal/fetch"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/steps"
	"github.com/jonsjava/anyserve/internal/supervise"
	"github.com/jonsjava/anyserve/internal/target"
)

// InstallRequest describes a new instance.
type InstallRequest struct {
	Service  string // "id" or "registry/id"
	Name     string
	Target   string
	Settings map[string]string
	Ports    map[string]int
	// ServerDir overrides where server files go.
	ServerDir string
	Start     bool
	// SteamOutput shows SteamCMD's full output.
	SteamOutput bool
	// Expose opens these private ports to the network (stacks only).
	Expose []string
	// Isolate runs a native server in a container that sees only its own
	// folder.
	Isolate bool
	// Domain serves the app over HTTPS through the proxy, forwarding to
	// DomainPort (empty picks the web port).
	Domain, DomainPort string
}

// Plan is a checked install request, ready to run. The wizard builds one
// first so it can show a summary before anything is downloaded.
type Plan struct {
	Req      InstallRequest
	Entry    Entry
	Target   target.Target
	Settings map[string]any
	Ports    map[string]int
	Warnings []string
	// Moved says which default ports were taken and what they moved to.
	Moved []string
}

// PlanInstall validates a request without changing anything.
func (a *App) PlanInstall(req InstallRequest) (*Plan, error) {
	e, err := a.Find(req.Service)
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		req.Name = e.Def.ID
	}
	if err := instance.ValidateName(req.Name); err != nil {
		return nil, err
	}
	if a.Store.Exists(req.Name) {
		return nil, fmt.Errorf("an instance named %q already exists; pick another name with --name", req.Name)
	}
	if req.Target == "" {
		req.Target = target.Default()
		if e.Def.IsStack() {
			req.Target = "docker"
		}
	}
	t, err := target.Get(req.Target)
	if err != nil {
		return nil, err
	}
	if err := t.Available(); err != nil {
		return nil, err
	}
	if err := t.SupportsService(e.Def); err != nil {
		return nil, err
	}
	settings, err := e.Def.ResolveSettings(req.Settings)
	if err != nil {
		return nil, err
	}
	ports, err := e.Def.ResolvePorts(req.Ports)
	if err != nil {
		return nil, err
	}
	p := &Plan{Req: req, Entry: e, Target: t, Settings: settings, Ports: ports}
	if req.Isolate {
		if err := target.CanIsolate(e.Def); err != nil {
			return nil, fmt.Errorf("--isolate: %w", err)
		}
	}
	if req.Domain != "" {
		a.moveOffRouter(req.Name, e.Def, req.DomainPort, req.Ports, ports)
		if err := a.checkDomain(&instance.Instance{Name: req.Name}, e.Def, req.Domain, req.DomainPort, ports); err != nil {
			return nil, fmt.Errorf("--domain: %w", err)
		}
	}
	if err := checkExpose(e.Def, req.Expose); err != nil {
		return nil, err
	}
	p.Moved = a.pickFreePorts(req.Name, e.Def, req.Ports, ports)
	conflicts, warnings := a.checkPorts(req.Name, e.Def, ports)
	if len(conflicts) > 0 {
		return nil, errors.New(strings.Join(conflicts, "\n") + "\nChoose other ports with --port name=number")
	}
	if sc := e.Def.Install.SteamCMD; sc != nil && sc.Login == "required" {
		if _, err := a.steamUser(); err != nil {
			return nil, fmt.Errorf("%s only downloads with a Steam account that owns it: %w", e.Def.Name, err)
		}
	}
	if !req.Isolate && t.Name() == target.Default() {
		var notes []string
		p.Req.Isolate, notes = runtimeCheck(e.Def, req.Name, runtime.GOOS, exec.LookPath, func() error { return target.CanIsolate(e.Def) })
		warnings = append(warnings, notes...)
	}
	p.Warnings = warnings
	return p, nil
}

// checkPorts finds clashes with other instances (errors) and ports already
// in use on this machine (warnings).
func (a *App) checkPorts(self string, d *spec.Definition, ports map[string]int) (conflicts, warnings []string) {
	others, _, _ := a.Store.List()
	for _, o := range others {
		if o.Name == self {
			continue
		}
		_, od, err := a.Store.Load(o.Name)
		if err != nil {
			continue
		}
		oports, err := od.ResolvePorts(o.Ports)
		if err != nil {
			continue
		}
		for _, p := range d.Ports {
			for _, op := range od.Ports {
				if ports[p.Name] == oports[op.Name] && protocolsOverlap(p.Protocol, op.Protocol) {
					conflicts = append(conflicts, fmt.Sprintf("port %d (%s) is already used by instance %q", ports[p.Name], p.Name, o.Name))
				}
			}
		}
	}
	if cfg, err := a.LoadConfig(); err == nil {
		for _, p := range d.Ports {
			if r, ok := reservedFor(cfg.Reserved, ports[p.Name], p.Protocol); ok {
				warnings = append(warnings, fmt.Sprintf("port %d (%s) is %s; it's yours to use, but check nothing else needs it", ports[p.Name], p.Name, describe(r)))
			}
		}
	}
	for _, p := range d.Ports {
		if busy(ports[p.Name], p.Protocol) {
			warnings = append(warnings, fmt.Sprintf("port %d (%s) is in use by something else on this machine right now", ports[p.Name], p.Name))
		}
	}
	return conflicts, warnings
}

func protocolsOverlap(a, b string) bool {
	return a == b || a == "both" || b == "both"
}

func busy(port int, proto string) bool {
	addr := ":" + strconv.Itoa(port)
	if proto == "tcp" || proto == "both" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return true
		}
		l.Close()
	}
	if proto == "udp" || proto == "both" {
		l, err := net.ListenPacket("udp", addr)
		if err != nil {
			return true
		}
		l.Close()
	}
	return false
}

// Install runs a plan: download, configure, register and optionally start.
func (a *App) Install(ctx context.Context, p *Plan) (*instance.Instance, error) {
	d := p.Entry.Def
	now := time.Now().UTC().Truncate(time.Second)
	in := &instance.Instance{
		Name:              p.Req.Name,
		Service:           d.ID,
		Registry:          p.Entry.Registry,
		DefinitionVersion: d.Version,
		Target:            p.Target.Name(),
		Settings:          map[string]string{},
		Ports:             p.Ports,
		Expose:            p.Req.Expose,
		Domain:            p.Req.Domain,
		DomainPort:        p.Req.DomainPort,
		Installed:         now,
		Updated:           now,
	}
	// Store every resolved value, including generated passwords, so later
	// runs use exactly what was installed.
	for k, v := range p.Settings {
		in.Settings[k] = fmt.Sprint(v)
	}
	dir := a.Store.Path(in.Name)
	in.ServerDir = p.Req.ServerDir
	customDir := in.ServerDir != ""
	if !customDir {
		in.ServerDir = filepath.Join(dir, "server")
	}
	abs, err := filepath.Abs(in.ServerDir)
	if err != nil {
		return nil, err
	}
	in.ServerDir = abs

	a.Log("Installing %s as %q", d.Name, in.Name)
	cleanup := func() {
		os.RemoveAll(dir)
	}
	if err := os.MkdirAll(in.ServerDir, 0o755); err != nil {
		cleanup()
		return nil, err
	}
	if err := a.Store.Save(in, d); err != nil {
		cleanup()
		return nil, err
	}
	if err := a.installFiles(ctx, in, d, p.Req.SteamOutput); err != nil {
		cleanup()
		return nil, err
	}
	if p.Req.Isolate {
		in.Isolation = target.IsolationContainer
		if _, err := target.EnsureNativeImage(d, a.Out); err != nil {
			cleanup()
			return nil, err
		}
		if err := a.Store.Save(in, d); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err := p.Target.Register(in, d, a.targetEnv()); err != nil {
		cleanup()
		return nil, fmt.Errorf("register with %s: %w", p.Target.Name(), err)
	}
	a.Log("Installed. Server files are in %s", in.ServerDir)
	if in.Domain != "" {
		if err := a.SyncDomains(); err != nil {
			a.Log("HTTPS for %s isn't set up: %v", in.Domain, err)
		} else {
			a.Log("https://%s will reach %s once the domain's DNS points here", in.Domain, in.Name)
		}
	}
	if p.Req.Start {
		if err := p.Target.Start(in); err != nil {
			return in, fmt.Errorf("installed, but starting failed: %w", err)
		}
		a.Log("Started %s", in.Name)
	}
	return in, nil
}

// installFiles fetches the server files, then runs steps and writes config.
func (a *App) installFiles(ctx context.Context, in *instance.Instance, d *spec.Definition, steamOutput bool) error {
	if d.IsStack() {
		// The software comes from images, which the docker target pulls.
		// A stack may still fetch pinned data files to mount.
		if err := a.fetchArchive(ctx, in, d); err != nil {
			return err
		}
		if err := a.fetchDownloads(ctx, in, d); err != nil {
			return err
		}
		return a.writeConfig(in, d)
	}
	switch {
	case d.Install.Archive != nil:
		if err := a.fetchArchive(ctx, in, d); err != nil {
			return err
		}
	case d.Install.SteamCMD != nil:
		sc := a.steamCMD()
		if steamOutput {
			sc.Output = a.Out
		}
		app := fetch.App{
			ID:       d.Install.SteamCMD.AppID,
			Dir:      in.ServerDir,
			Platform: d.Install.SteamCMD.Platform[runtime.GOOS],
			Beta:     d.Install.SteamCMD.Beta,
		}
		if d.Install.SteamCMD.Login == "required" {
			user, err := a.steamUser()
			if err != nil {
				return err
			}
			app.User = user
		}
		if err := sc.AppUpdate(ctx, app); err != nil {
			return err
		}
	}
	if err := a.fetchDownloads(ctx, in, d); err != nil {
		return err
	}
	if err := steps.Apply(d, in.ServerDir, runtime.GOOS, steps.Options{SteamCMDDir: a.steamCMD().Dir}); err != nil {
		return err
	}
	if err := a.writeConfig(in, d); err != nil {
		return err
	}
	tctx, err := in.Context(d, runtime.GOOS)
	if err != nil {
		return err
	}
	out := io.Discard
	if a.Out != nil {
		out = a.Out
	}
	return steps.RunExec(ctx, d, in.ServerDir, runtime.GOOS, tctx, out)
}

// fetchArchive downloads and extracts install.archive, if there is one.
func (a *App) fetchArchive(ctx context.Context, in *instance.Instance, d *spec.Definition) error {
	ar := d.Install.Archive
	if ar == nil {
		return nil
	}
	file, err := fetch.Download(ctx, ar.URL, ar.SHA256, a.Dirs.Downloads(), a.Log)
	if err != nil {
		return err
	}
	sub := ar.Subdir[runtime.GOOS]
	if sub == "" {
		sub = ar.Subdir["*"]
	}
	dest := in.ServerDir
	if ar.To != "" {
		if dest, err = steps.Resolve(in.ServerDir, ar.To); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	}
	keep := make([]string, len(ar.Keep))
	for i, k := range ar.Keep {
		// Keep paths are relative to the server folder.
		keep[i] = k
		if ar.To != "" {
			if rel, err := filepath.Rel(filepath.FromSlash(ar.To), filepath.FromSlash(k)); err == nil {
				keep[i] = filepath.ToSlash(rel)
			}
		}
	}
	n, err := fetch.ExtractAs(file, ar.Format, sub, dest, keep)
	if err != nil {
		return err
	}
	a.Log("Extracted %d files", n)
	return nil
}

// fetchDownloads fetches install.downloads that aren't there yet (or are
// marked always).
func (a *App) fetchDownloads(ctx context.Context, in *instance.Instance, d *spec.Definition) error {
	for _, dl := range d.Install.Downloads {
		dst, err := steps.Resolve(in.ServerDir, dl.To)
		if err != nil {
			return err
		}
		if _, err := os.Stat(dst); err == nil && !dl.Always {
			continue
		}
		file, err := fetch.Download(ctx, dl.URL, dl.SHA256, a.Dirs.Downloads(), a.Log)
		if err != nil {
			return err
		}
		if err := copyInto(file, dst); err != nil {
			return err
		}
		a.Log("Added %s", dl.To)
	}
	return nil
}

func (a *App) writeConfig(in *instance.Instance, d *spec.Definition) error {
	ctx, err := in.Context(d, runtime.GOOS)
	if err != nil {
		return err
	}
	written, pending, err := steps.WriteFiles(d, in.ServerDir, ctx)
	if err != nil {
		return err
	}
	if len(written) > 0 {
		a.Log("Wrote %s", strings.Join(written, ", "))
	}
	if len(pending) > 0 {
		a.Log("The server creates %s on first start; Anyserve applies your settings to it on the next start", strings.Join(pending, ", "))
	}
	return nil
}

func (a *App) steamCMD() fetch.SteamCMD {
	return fetch.SteamCMD{Dir: filepath.Join(a.Dirs.Tools(), "steamcmd"), Cache: a.Dirs.Downloads(), Log: a.Log}
}

// steamUser returns the Steam account saved by `anyserve steam login`.
func (a *App) steamUser() (string, error) {
	c, err := a.LoadConfig()
	if err != nil {
		return "", err
	}
	if c.SteamUser == "" {
		return "", errors.New("sign in first with `anyserve steam login <steam-user>`")
	}
	return c.SteamUser, nil
}

// SteamLogin signs in to Steam through SteamCMD, which caches the session.
// Only the user name is saved; SteamCMD handles the password and Steam
// Guard itself.
func (a *App) SteamLogin(ctx context.Context, user string) error {
	if user == "" || strings.ContainsAny(user, " \t\r\n+") {
		return fmt.Errorf("%q isn't a Steam user name", user)
	}
	if err := a.steamCMD().Login(ctx, user); err != nil {
		return fmt.Errorf("Steam login failed: %w", err)
	}
	c, err := a.LoadConfig()
	if err != nil {
		return err
	}
	c.SteamUser = user
	return a.SaveConfig(c)
}

// SteamLogout forgets the saved Steam user.
func (a *App) SteamLogout() error {
	c, err := a.LoadConfig()
	if err != nil {
		return err
	}
	c.SteamUser = ""
	return a.SaveConfig(c)
}

func copyInto(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".anyserve-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// TargetEnv is what targets need to write services that run this binary.
func (a *App) TargetEnv() target.Env { return a.targetEnv() }

func (a *App) targetEnv() target.Env {
	exe, err := os.Executable()
	if err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
	}
	env := target.Env{Exe: exe, Out: a.Out}
	if h := os.Getenv("ANYSERVE_HOME"); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			env.Home = abs
		}
	}
	return env
}

// load returns an instance with its definition and target.
func (a *App) load(name string) (*instance.Instance, *spec.Definition, target.Target, error) {
	in, d, err := a.Store.Load(name)
	if err != nil {
		return nil, nil, nil, err
	}
	t, err := target.Get(in.Target)
	if err != nil {
		return nil, nil, nil, err
	}
	return in, d, t, nil
}

// Start starts an instance.
func (a *App) Start(name string) error {
	in, _, t, err := a.load(name)
	if err != nil {
		return err
	}
	return t.Start(in)
}

// Stop stops an instance cleanly.
func (a *App) Stop(name string) error {
	in, _, t, err := a.load(name)
	if err != nil {
		return err
	}
	return t.Stop(in)
}

// Restart stops then starts an instance, warning players first if asked.
func (a *App) Restart(name string, warn time.Duration) error {
	in, d, t, err := a.load(name)
	if err != nil {
		return err
	}
	if warn > 0 && t.Status(in).State == "running" {
		a.warnPlayers(in, d, warn, "restart")
	}
	if err := t.Stop(in); err != nil {
		return err
	}
	return t.Start(in)
}

// warnPlayers broadcasts a countdown before a restart or update.
func (a *App) warnPlayers(in *instance.Instance, d *spec.Definition, total time.Duration, what string) {
	if d.Console.Say == "" {
		a.Log("%s can't broadcast messages; skipping the player warning", d.Name)
		return
	}
	marks := countdown(total)
	for i, left := range marks {
		msg := fmt.Sprintf("Server %s in %s.", what, humanDuration(left))
		s, err := spec.Render("console.say", d.Console.Say, spec.Context{Message: msg})
		if err != nil {
			a.Log("Couldn't warn players: %v", err)
			return
		}
		if err := supervise.Send(in.ConsoleSocket(), s); err != nil {
			a.Log("Couldn't warn players: %v", err)
			return
		}
		a.Log("Warned players: %s", msg)
		next := time.Duration(0)
		if i+1 < len(marks) {
			next = marks[i+1]
		}
		time.Sleep(left - next)
	}
}

// countdown returns the moments (time remaining) to warn at.
func countdown(total time.Duration) []time.Duration {
	marks := []time.Duration{15 * time.Minute, 10 * time.Minute, 5 * time.Minute, time.Minute, 30 * time.Second, 10 * time.Second}
	out := []time.Duration{total}
	for _, m := range marks {
		if m < total {
			out = append(out, m)
		}
	}
	return out
}

func humanDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		n := int(d / time.Minute)
		if n == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", n)
	}
	return fmt.Sprintf("%d seconds", int(d/time.Second))
}

// StatusLine summarizes one instance for `list` and `status`.
type StatusLine struct {
	Instance *instance.Instance
	Def      *spec.Definition
	Status   target.Status
	// Upgrade is the newer definition version available, if any.
	Upgrade string
}

// upgrade returns the registry's definition version when it differs from
// the installed one (a new version, or the same version with changes such
// as new image digests).
func (a *App) upgrade(in *instance.Instance, d *spec.Definition) string {
	e, err := a.Find(in.Registry + "/" + in.Service)
	if err != nil || !DefinitionChanged(d, e.Def) {
		return ""
	}
	if e.Def.Version == in.DefinitionVersion {
		return e.Def.Version + " (updated)"
	}
	return e.Def.Version
}

// Statuses reports on every instance.
func (a *App) Statuses() ([]StatusLine, map[string]error, error) {
	list, broken, err := a.Store.List()
	if err != nil {
		return nil, nil, err
	}
	var out []StatusLine
	for _, in := range list {
		_, d, t, err := a.load(in.Name)
		if err != nil {
			broken[in.Name] = err
			continue
		}
		sl := StatusLine{Instance: in, Def: d, Status: t.Status(in)}
		sl.Upgrade = a.upgrade(in, d)
		out = append(out, sl)
	}
	return out, broken, nil
}

// Status reports on one instance.
func (a *App) Status(name string) (StatusLine, error) {
	in, d, t, err := a.load(name)
	if err != nil {
		return StatusLine{}, err
	}
	sl := StatusLine{Instance: in, Def: d, Status: t.Status(in)}
	sl.Upgrade = a.upgrade(in, d)
	return sl, nil
}

// Logs shows an instance's logs.
func (a *App) Logs(name string, lines int, follow bool) error {
	in, _, t, err := a.load(name)
	if err != nil {
		return err
	}
	return t.Logs(in, lines, follow, a.Out)
}

// Say broadcasts a chat message to players.
func (a *App) Say(name, msg string) error {
	in, d, _, err := a.load(name)
	if err != nil {
		return err
	}
	if d.Console.Say == "" {
		return fmt.Errorf("%s doesn't support broadcasting messages", d.Name)
	}
	if d.IsStack() {
		return fmt.Errorf("%s is a container stack and has no console", d.Name)
	}
	s, err := spec.Render("console.say", d.Console.Say, spec.Context{Message: msg})
	if err != nil {
		return err
	}
	return a.sendLine(in, d, s)
}

// Run supervises an instance in the foreground. Service managers call this.
func (a *App) Run(ctx context.Context, name string) error {
	in, d, _, err := a.load(name)
	if err != nil {
		return err
	}
	if d.IsStack() {
		return fmt.Errorf("%s is a container stack; Docker runs it, not `anyserve run`", d.Name)
	}
	tctx, err := in.Context(d, runtime.GOOS)
	if err != nil {
		return err
	}
	r, err := d.RenderRun(runtime.GOOS, tctx)
	if err != nil {
		return err
	}
	cmdPath, err := steps.Resolve(in.ServerDir, r.Command)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cmdPath); err != nil {
		return fmt.Errorf("%s: %w", cmdPath, steps.ErrNotInstalled)
	}
	workdir := in.ServerDir
	if r.Workdir != "" {
		if workdir, err = steps.Resolve(in.ServerDir, r.Workdir); err != nil {
			return err
		}
	}
	// Games that create their config on first start get the user's settings
	// applied before every start.
	if err := a.writeConfig(in, d); err != nil {
		return err
	}
	isolated := in.Isolation == target.IsolationContainer
	command, args := cmdPath, r.Args
	if len(r.Via) > 0 {
		var chain []string
		for _, v := range r.Via {
			l := spec.Launchers[v]
			prog := l.Program // in a container, found on the image's PATH
			if !isolated {
				if prog, err = exec.LookPath(l.Program); err != nil {
					return fmt.Errorf("%s needs %s, which isn't installed: install %s", d.Name, l.Program, l.Install)
				}
			}
			chain = append(chain, prog)
			chain = append(chain, l.Args...)
		}
		chain = append(chain, r.ViaArgs...)
		chain = append(chain, cmdPath)
		command, args = chain[0], append(chain[1:], r.Args...)
	}
	env := os.Environ()
	if runtime.GOOS != "windows" {
		// Each server gets its own home, so per-user files (Steam SDK
		// libraries, saves under ~/.local/share, ~/.config) stay inside the
		// instance and two instances of a game don't collide.
		env = append(env, "HOME="+in.ServerDir)
	}
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+r.Env[k])
	}
	if isolated {
		if err := target.CanIsolate(d); err != nil {
			return fmt.Errorf("can't run %s in a container: %w", in.Name, err)
		}
		image, err := target.EnsureNativeImage(d, a.Out)
		if err != nil {
			return err
		}
		var own []string
		for _, k := range keys {
			own = append(own, k+"="+r.Env[k])
		}
		target.RemoveIsolated(in)
		defer target.RemoveIsolated(in)
		command, args = target.IsolatedCommand(in, d, image, workdir, command, args, own)
	}
	timeout := time.Duration(d.Stop.TimeoutSeconds) * time.Second
	var stopFunc func() error
	if d.Stop.RCON {
		stopFunc = func() error {
			err := a.sendLine(in, d, d.Stop.Command)
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
				// The server closed the connection as it shut down.
				return nil
			}
			return err
		}
	}
	return supervise.Run(ctx, supervise.Options{
		StopFunc:    stopFunc,
		Command:     command,
		Args:        args,
		Dir:         workdir,
		Env:         env,
		StopCommand: d.Stop.Command,
		StopTimeout: timeout,
		Socket:      in.ConsoleSocket(),
		LogDir:      in.LogDir(),
		Stdout:      a.Out,
		Log:         func(f string, args ...any) { fmt.Fprintf(a.Out, "[anyserve] "+f+"\n", args...) },
	})
}

// Update re-downloads the server, applies the newest definition from the
// registry, and restarts the server if it was running.
func (a *App) Update(ctx context.Context, name string, warn time.Duration, steamOutput bool) error {
	in, d, t, err := a.load(name)
	if err != nil {
		return err
	}
	e, err := a.Find(in.Registry + "/" + in.Service)
	var newer *NeedsNewerError
	if errors.As(err, &newer) {
		// The registry's newer definition needs a newer Anyserve; keep
		// updating the server files with the one installed.
		a.Log("Keeping definition %s: %v", in.DefinitionVersion, err)
	}
	if err == nil && DefinitionChanged(d, e.Def) {
		if e.Def.Version == in.DefinitionVersion {
			a.Log("Applying the registry's changes to definition %s", e.Def.Version)
		} else {
			a.Log("Applying definition %s (was %s)", e.Def.Version, in.DefinitionVersion)
		}
		// New settings in the definition get their defaults.
		if _, err := e.Def.ResolveSettings(knownSettings(e.Def, in.Settings)); err != nil {
			return fmt.Errorf("the new definition needs a setting change first: %w", err)
		}
		d = e.Def
		in.Settings = knownSettings(d, in.Settings)
		in.DefinitionVersion = d.Version
	}
	wasRunning := t.Status(in).State == "running"
	if wasRunning {
		if warn > 0 {
			a.warnPlayers(in, d, warn, "update")
		}
		a.Log("Stopping %s", in.Name)
		if err := t.Stop(in); err != nil {
			return err
		}
	}
	if err := a.installFiles(ctx, in, d, steamOutput); err != nil {
		return err
	}
	resolved, err := d.ResolveSettings(in.Settings)
	if err != nil {
		return err
	}
	for k, v := range resolved {
		in.Settings[k] = fmt.Sprint(v)
	}
	in.Updated = time.Now().UTC().Truncate(time.Second)
	// An update that goes through clears any hold from a failed one.
	in.HeldDefinition, in.HeldBuild = "", ""
	if err := a.Store.Save(in, d); err != nil {
		return err
	}
	env := a.targetEnv()
	env.Out = nil // the lingering hint was already shown at install
	if err := t.Register(in, d, env); err != nil {
		return err
	}
	if wasRunning {
		a.Log("Starting %s", in.Name)
		return t.Start(in)
	}
	return nil
}

// knownSettings drops settings the definition no longer has.
func knownSettings(d *spec.Definition, raw map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range raw {
		if _, ok := d.Setting(k); ok {
			out[k] = v
		}
	}
	return out
}

// Reconfigure changes settings or ports, rewrites config files and restarts
// the server if it was running.
// Changes are what `anyserve config` can change on an instance.
type Changes struct {
	Settings map[string]string
	Ports    map[string]int
	// Expose and Unexpose open private ports to the network, or close
	// them again (stacks only).
	Expose, Unexpose []string
	// Isolate turns container isolation on or off (nil leaves it).
	Isolate *bool
	// Domain sets (or, empty, removes) the HTTPS domain; nil leaves it.
	Domain     *string
	DomainPort *string
}

// checkExpose checks that ports named for --expose exist and can be
// exposed.
func checkExpose(d *spec.Definition, names []string) error {
	for _, n := range names {
		p, ok := d.Port(n)
		if !ok {
			return fmt.Errorf("%s has no port %q", d.Name, n)
		}
		if !d.IsStack() {
			return fmt.Errorf("--expose is for stacks; %s's ports are opened by the server itself", d.Name)
		}
		if p.Public {
			return fmt.Errorf("port %q is already open to the network", n)
		}
	}
	return nil
}

// Reconfigure applies changes and restarts the instance if it was running.
func (a *App) Reconfigure(name string, c Changes) error {
	settings, ports := c.Settings, c.Ports
	in, d, t, err := a.load(name)
	if err != nil {
		return err
	}
	if err := checkExpose(d, c.Expose); err != nil {
		return err
	}
	if err := checkExpose(d, c.Unexpose); err != nil {
		return err
	}
	expose := map[string]bool{}
	for _, p := range in.Expose {
		expose[p] = true
	}
	for _, p := range c.Expose {
		expose[p] = true
	}
	for _, p := range c.Unexpose {
		delete(expose, p)
	}
	in.Expose = nil
	for _, p := range d.Ports {
		if expose[p.Name] {
			in.Expose = append(in.Expose, p.Name)
		}
	}
	merged := map[string]string{}
	for k, v := range in.Settings {
		merged[k] = v
	}
	for k, v := range settings {
		merged[k] = v
	}
	if _, err := d.ResolveSettings(merged); err != nil {
		return err
	}
	newPorts := map[string]int{}
	for k, v := range in.Ports {
		newPorts[k] = v
	}
	for k, v := range ports {
		newPorts[k] = v
	}
	resolvedPorts, err := d.ResolvePorts(newPorts)
	if err != nil {
		return err
	}
	if conflicts, _ := a.checkPorts(in.Name, d, resolvedPorts); len(conflicts) > 0 {
		return errors.New(strings.Join(conflicts, "\n"))
	}
	in.Settings, in.Ports = merged, resolvedPorts
	domainChanged := false
	if c.Domain != nil || c.DomainPort != nil || (in.Domain != "" && len(ports) > 0) {
		dom, dport := in.Domain, in.DomainPort
		if c.Domain != nil {
			dom = strings.ToLower(strings.TrimSpace(*c.Domain))
		}
		if c.DomainPort != nil {
			dport = *c.DomainPort
		}
		if err := a.checkDomain(in, d, dom, dport, resolvedPorts); err != nil {
			return err
		}
		in.Domain, in.DomainPort = dom, dport
		if dom == "" {
			in.DomainPort = ""
		}
		domainChanged = true
	}
	if c.Isolate != nil {
		if *c.Isolate {
			if err := target.CanIsolate(d); err != nil {
				return err
			}
			if _, err := target.EnsureNativeImage(d, a.Out); err != nil {
				return err
			}
			in.Isolation = target.IsolationContainer
		} else {
			in.Isolation = ""
		}
	}
	if err := a.Store.Save(in, d); err != nil {
		return err
	}
	if err := a.writeConfig(in, d); err != nil {
		return err
	}
	if d.IsStack() {
		// Settings, ports and exposure live in the compose file.
		if err := t.Register(in, d, a.targetEnv()); err != nil {
			return err
		}
	}
	if domainChanged || len(ports) > 0 {
		if err := a.SyncDomains(); err != nil {
			return fmt.Errorf("saved, but the HTTPS proxy wasn't updated: %w", err)
		}
		if domainChanged && len(settings) == 0 && len(ports) == 0 && c.Isolate == nil && len(c.Expose) == 0 && len(c.Unexpose) == 0 {
			return nil // nothing about the app itself changed
		}
	}
	if t.Status(in).State == "running" {
		a.Log("Restarting %s to apply the change", in.Name)
		if err := t.Stop(in); err != nil {
			return err
		}
		return t.Start(in)
	}
	return nil
}

// Remove stops and deletes an instance. Backups are kept.
func (a *App) Remove(name string) error {
	in, _, t, err := a.load(name)
	if err != nil {
		return err
	}
	if err := t.Unregister(in); err != nil {
		return err
	}
	customDir := !strings.HasPrefix(in.ServerDir, in.Dir+string(filepath.Separator))
	if err := a.Store.Delete(name); err != nil {
		return err
	}
	if customDir {
		a.Log("Server files in %s were left in place", in.ServerDir)
	}
	if in.Domain != "" {
		if err := a.SyncDomains(); err != nil {
			a.Log("Couldn't take %s off the HTTPS proxy: %v", in.Domain, err)
		}
	}
	return nil
}

// PortReport describes what to forward or open for an instance.
func (a *App) PortReport(name string) (string, error) {
	in, d, _, err := a.load(name)
	if err != nil {
		return "", err
	}
	ports, err := d.ResolvePorts(in.Ports)
	if err != nil {
		return "", err
	}
	return FormatPortsShared(d, ports, a.SharedPorts(d, in.Domain, in.DomainPort), in.Expose), nil
}

// SharedPorts maps each of an instance's ports that players reach through
// a shared listener (the Minecraft router, or Caddy on 443) to that
// listener's port, which is then the one to forward.
func (a *App) SharedPorts(d *spec.Definition, domain, domainPort string) map[string]int {
	if domain == "" {
		return nil
	}
	t, err := TargetFor(d, domainPort)
	if err != nil {
		return nil
	}
	switch t.Kind {
	case spec.RouteMinecraft:
		return map[string]int{t.Port.Name: a.routerPort()}
	case spec.RouteTLS:
		return map[string]int{t.Port.Name: 443}
	}
	return nil
}

// runtimeCheck looks for the programs a native server is started through
// (such as Java). When one is missing and the server can run in its
// definition's container, which has it, it reports isolate rather than
// installing something that can't start; otherwise it warns.
func runtimeCheck(d *spec.Definition, name, goos string, lookPath func(string) (string, error), canIsolate func() error) (isolate bool, notes []string) {
	r, ok := d.Run[goos]
	if !ok {
		return false, nil
	}
	var missing []spec.Launcher
	for _, l := range r.Requires() {
		if _, err := lookPath(l.Program); err != nil {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	if d.Container != nil && canIsolate() == nil {
		var progs []string
		for _, l := range missing {
			progs = append(progs, l.Program)
		}
		return true, []string{fmt.Sprintf("%s isn't installed here, so %s will run in a container that has it (%s). To run it directly instead, install it and then run `anyserve config %s --no-isolate`.", strings.Join(progs, " and "), d.Name, d.Container.Image, name)}
	}
	for _, l := range missing {
		notes = append(notes, fmt.Sprintf("%s needs %s, which isn't installed, and won't start until it is: install %s", d.Name, l.Program, l.Install))
	}
	return false, notes
}

// FormatPorts explains which ports to forward and which to keep private.
func FormatPorts(d *spec.Definition, ports map[string]int) string {
	return FormatPortsShared(d, ports, nil, nil)
}

// FormatPortsShared is FormatPorts for an instance whose ports in shared
// are reached through a shared listener on the given port, and whose
// private ports in exposed are open to the network.
func FormatPortsShared(d *spec.Definition, ports map[string]int, shared map[string]int, exposed []string) string {
	var b strings.Builder
	open := map[string]bool{}
	for _, n := range exposed {
		open[n] = true
	}
	var public []string
	seen := map[string]bool{}
	for _, p := range d.Ports {
		vis := "keep private"
		if open[p.Name] {
			vis = "open to network"
		}
		if p.Public {
			vis = "forward on router"
			n := ports[p.Name]
			if s, ok := shared[p.Name]; ok {
				vis = fmt.Sprintf("reached on %d", s)
				n = s
			}
			for _, proto := range protos(p.Protocol) {
				if e := fmt.Sprintf("%d/%s", n, proto); !seen[e] {
					seen[e] = true
					public = append(public, e)
				}
			}
		}
		fmt.Fprintf(&b, "  %-6s %5d/%-4s %-20s %s\n", p.Name, ports[p.Name], p.Protocol, vis, p.Description)
	}
	if len(public) > 0 {
		fmt.Fprintf(&b, "\nSo people can connect from the internet, forward %s on your router to this machine.\n", strings.Join(public, ", "))
		fmt.Fprintf(&b, "If this machine runs a firewall, allow them too. For example:\n")
		var ufw, fwd []string
		for _, p := range public {
			ufw = append(ufw, "sudo ufw allow "+p)
			fwd = append(fwd, "--add-port="+p)
		}
		fmt.Fprintf(&b, "  ufw:       %s\n", strings.Join(ufw, " && "))
		fmt.Fprintf(&b, "  firewalld: sudo firewall-cmd --permanent %s && sudo firewall-cmd --reload\n", strings.Join(fwd, " "))
	}
	return b.String()
}

func protos(p string) []string {
	if p == "both" {
		return []string{"tcp", "udp"}
	}
	return []string{p}
}
