package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonsjava/anyserve/internal/https"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// HTTPSConfig chooses the HTTPS reverse proxy.
type HTTPSConfig struct {
	// Provider is caddy (the default: Anyserve runs it), nginx or traefik
	// (Anyserve writes a config file for a proxy you run), or none.
	Provider string `json:"provider,omitempty"`
	// Email is given to Let's Encrypt for expiry warnings (optional).
	Email string `json:"email,omitempty"`
}

// RouterConfig is the Minecraft hostname router's settings.
type RouterConfig struct {
	// Port is where players connect (default 25565).
	Port int `json:"port,omitempty"`
}

// HTTPSProvider returns the configured provider, defaulting to Caddy.
func (c *Config) HTTPSProvider() string {
	if c.HTTPS.Provider == "" {
		return https.Caddy
	}
	return c.HTTPS.Provider
}

func (a *App) httpsDir() string { return filepath.Join(a.Dirs.Data, "https") }

// HTTPSFile is where the config for a user-run proxy is written.
func (a *App) HTTPSFile(provider string) string {
	switch provider {
	case https.Nginx:
		return filepath.Join(a.httpsDir(), "anyserve-nginx.conf")
	case https.Traefik:
		return filepath.Join(a.httpsDir(), "anyserve-traefik.yml")
	}
	return filepath.Join(a.httpsDir(), "Caddyfile")
}

// WebPort picks the port a domain forwards to: the named one, or else a
// port called http or web, or else the only private TCP port.
func WebPort(d *spec.Definition, named string) (spec.Port, error) {
	if named != "" {
		p, ok := d.Port(named)
		if !ok {
			return spec.Port{}, fmt.Errorf("%s has no port %q", d.Name, named)
		}
		if p.Protocol == "udp" {
			return spec.Port{}, fmt.Errorf("port %s is UDP; HTTPS needs a TCP port", named)
		}
		return p, nil
	}
	var candidates []spec.Port
	for _, p := range d.Ports {
		if p.Name == "http" || p.Name == "web" {
			return p, nil
		}
		if !p.Public && p.Protocol != "udp" {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return spec.Port{}, fmt.Errorf("%s has no obvious web port; say which with --domain-port", d.Name)
}

// DomainTarget is the port an instance's domain reaches, and how.
type DomainTarget struct {
	Port spec.Port
	// Kind is http, tls or minecraft, or "" when the domain is only a
	// DNS name pointing at this machine.
	Kind string
}

// TargetFor picks what an instance's domain reaches: the named port, or
// else the first port with routing, or else (for apps) the web port. A
// native server with no routable port gets a DNS-only domain.
func TargetFor(d *spec.Definition, named string) (DomainTarget, error) {
	if named != "" {
		p, ok := d.Port(named)
		if !ok {
			return DomainTarget{}, fmt.Errorf("%s has no port %q", d.Name, named)
		}
		kind := p.Routing
		if kind == "" && d.IsStack() {
			if p.Protocol == "udp" {
				return DomainTarget{}, fmt.Errorf("port %s is UDP; HTTPS needs a TCP port", named)
			}
			kind = spec.RouteHTTP
		}
		return DomainTarget{Port: p, Kind: kind}, nil
	}
	for _, p := range d.Ports {
		if p.Routing != "" {
			return DomainTarget{Port: p, Kind: p.Routing}, nil
		}
	}
	if d.IsStack() {
		p, err := WebPort(d, "")
		if err != nil {
			return DomainTarget{}, err
		}
		return DomainTarget{Port: p, Kind: spec.RouteHTTP}, nil
	}
	return DomainTarget{}, nil
}

// Route is one domain and the local port it reaches.
type Route struct {
	https.Site
	Kind string
}

// Routes lists every instance's domain with what it reaches.
func (a *App) Routes() ([]Route, error) {
	list, _, err := a.Store.List()
	if err != nil {
		return nil, err
	}
	var out []Route
	for _, in := range list {
		if in.Domain == "" {
			continue
		}
		_, d, err := a.Store.Load(in.Name)
		if err != nil {
			continue
		}
		t, err := TargetFor(d, in.DomainPort)
		if err != nil {
			a.Log("Skipping %s's domain: %v", in.Name, err)
			continue
		}
		ports, err := d.ResolvePorts(in.Ports)
		if err != nil {
			continue
		}
		port := 0
		if t.Kind != "" {
			port = ports[t.Port.Name]
		}
		out = append(out, Route{Site: https.Site{Domain: in.Domain, Port: port, Instance: in.Name}, Kind: t.Kind})
	}
	return out, nil
}

// Sites lists the domains the HTTPS proxy serves.
func (a *App) Sites() ([]https.Site, error) { return a.sitesOf(spec.RouteHTTP) }

func (a *App) sitesOf(kind string) ([]https.Site, error) {
	routes, err := a.Routes()
	if err != nil {
		return nil, err
	}
	var out []https.Site
	for _, r := range routes {
		if r.Kind == kind {
			out = append(out, r.Site)
		}
	}
	return out, nil
}

// checkDomain validates a domain change for an instance.
func (a *App) checkDomain(in *instance.Instance, d *spec.Definition, domain, port string, ports map[string]int) error {
	if domain == "" {
		return nil
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if err := https.ValidDomain(domain); err != nil {
		return err
	}
	t, err := TargetFor(d, port)
	if err != nil {
		return err
	}
	if (t.Kind == spec.RouteHTTP || t.Kind == spec.RouteTLS) && cfg.HTTPSProvider() == https.None {
		return errors.New("the HTTPS proxy is off (`anyserve https use caddy` turns it on)")
	}
	if t.Kind == spec.RouteTLS && cfg.HTTPSProvider() != https.Caddy {
		return fmt.Errorf("passing TLS through by name needs Anyserve's Caddy (`anyserve https use caddy`); %s can't share port 443 that way here", cfg.HTTPSProvider())
	}
	if t.Kind == spec.RouteMinecraft && ports[t.Port.Name] == a.routerPort() {
		return fmt.Errorf("the Minecraft router takes port %d for every server with a domain, so this server needs a port of its own: add --port %s=<another number>", a.routerPort(), t.Port.Name)
	}
	list, _, err := a.Store.List()
	if err != nil {
		return err
	}
	for _, o := range list {
		if o.Name != in.Name && o.Domain == domain {
			return fmt.Errorf("%s already uses %s", o.Name, domain)
		}
	}
	return nil
}

// SyncHTTPS brings the proxy in line with the instances' domains: it
// configures and (re)loads Anyserve's Caddy, or rewrites the config file
// for a user-run nginx or Traefik.
// SyncDomains brings the HTTPS proxy and the Minecraft router in line
// with the instances' domains.
func (a *App) SyncDomains() error {
	return errors.Join(a.syncProxy(), a.syncRouter())
}

func (a *App) routerDir() string { return filepath.Join(a.Dirs.Data, "router") }

// syncRouter starts, updates or stops the Minecraft router.
func (a *App) syncRouter() error {
	sites, err := a.sitesOf(spec.RouteMinecraft)
	if err != nil {
		return err
	}
	routes := map[string]int{}
	for _, s := range sites {
		routes[s.Domain] = s.Port
	}
	r := https.Router{Dir: a.routerDir()}
	port := a.routerPort()
	if len(routes) > 0 && !r.Running() {
		if !https.PortFree(port) {
			return fmt.Errorf("port %d is taken by another program, so the Minecraft router can't start. Free it, or give the router another port (`anyserve router port <n>`)", port)
		}
		a.Log("Starting the Minecraft router on port %d (players connect to each server's domain)", port)
	}
	if err := r.Apply(port, routes); err != nil {
		return err
	}
	if r.Restarted {
		a.Log("Restarted the Minecraft router to drop an old route; players on servers with domains reconnect")
	}
	return nil
}

// RouterRunning reports whether the Minecraft router is up.
func (a *App) RouterRunning() bool { return https.Router{Dir: a.routerDir()}.Running() }

// SetRouterPort changes the port the Minecraft router listens on.
func (a *App) SetRouterPort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("the port must be 1-65535")
	}
	routes, err := a.Routes()
	if err != nil {
		return err
	}
	for _, r := range routes {
		if r.Kind == spec.RouteMinecraft && r.Port == port {
			return fmt.Errorf("%s's game port is %d; move it first (`anyserve config %s --port game=<n>`)", r.Instance, port, r.Instance)
		}
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if port == https.DefaultRouterPort {
		port = 0
	}
	cfg.Router.Port = port
	if err := a.SaveConfig(cfg); err != nil {
		return err
	}
	// A new port means a new container.
	if err := (https.Managed{Dir: a.routerDir()}).Stop(); err != nil {
		return err
	}
	return a.syncRouter()
}

func (a *App) syncProxy() error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	sites, err := a.Sites()
	if err != nil {
		return err
	}
	provider := cfg.HTTPSProvider()
	m := https.Managed{Dir: a.httpsDir()}
	if provider != https.Caddy {
		if err := m.Stop(); err != nil {
			a.Log("Stopping Anyserve's Caddy: %v", err)
		}
	}
	switch provider {
	case https.None:
		return nil
	case https.Nginx, https.Traefik:
		body := https.NginxConf(sites)
		if provider == https.Traefik {
			body = https.TraefikConf(sites)
		}
		if err := os.MkdirAll(a.httpsDir(), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(a.HTTPSFile(provider), []byte(body), 0o644); err != nil {
			return err
		}
		if len(sites) > 0 {
			a.Log("Wrote %s for your %s (%d domains); reload %s to apply it", a.HTTPSFile(provider), provider, len(sites), provider)
		}
		return nil
	}
	if len(sites) > 0 && !m.Running() {
		for _, port := range []int{80, 443} {
			if !https.PortFree(port) {
				return fmt.Errorf("port %d is taken by another program, so Caddy can't serve HTTPS. Stop it, or use it as your proxy instead (`anyserve https use nginx` or `traefik` writes its config)", port)
			}
		}
		a.Log("Starting Caddy for HTTPS (certificates come from Let's Encrypt; ports 80 and 443 must reach this machine)")
	}
	return m.Apply(cfg.HTTPS.Email, sites)
}

// SetHTTPSProvider changes the proxy and syncs it.
func (a *App) SetHTTPSProvider(provider, email string, setEmail bool) error {
	ok := false
	for _, p := range https.Providers {
		ok = ok || p == provider
	}
	if !ok {
		return fmt.Errorf("choose one of %s", strings.Join(https.Providers, ", "))
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if provider == https.None {
		if sites, _ := a.Sites(); len(sites) > 0 {
			return fmt.Errorf("%d instances have domains; remove them first (`anyserve config <name> --no-domain`)", len(sites))
		}
	}
	cfg.HTTPS.Provider = provider
	if setEmail {
		cfg.HTTPS.Email = email
	}
	if err := a.SaveConfig(cfg); err != nil {
		return err
	}
	return a.syncProxy()
}

// HTTPSRunning reports whether Anyserve's Caddy is running.
func (a *App) HTTPSRunning() bool { return https.Managed{Dir: a.httpsDir()}.Running() }

// routerPort is where the Minecraft router listens.
func (a *App) routerPort() int {
	if c, err := a.LoadConfig(); err == nil && c.Router.Port != 0 {
		return c.Router.Port
	}
	return https.DefaultRouterPort
}

// moveOffRouter gives a Minecraft server with a domain a free port of its
// own when it would otherwise sit on the router's port and the user didn't
// pick one: players use the router's port, so the server's own port
// doesn't matter to them.
func (a *App) moveOffRouter(name string, d *spec.Definition, domainPort string, given, ports map[string]int) {
	t, err := TargetFor(d, domainPort)
	if err != nil || t.Kind != spec.RouteMinecraft {
		return
	}
	if _, ok := given[t.Port.Name]; ok || ports[t.Port.Name] != a.routerPort() {
		return
	}
	for n := a.routerPort() + 1; n < a.routerPort()+100; n++ {
		ports[t.Port.Name] = n
		if _, err := d.ResolvePorts(ports); err != nil {
			continue
		}
		conflicts, warnings := a.checkPorts(name, d, ports)
		clash := false
		for _, m := range append(conflicts, warnings...) {
			if strings.Contains(m, fmt.Sprintf("port %d (%s)", n, t.Port.Name)) {
				clash = true
			}
		}
		if !clash {
			a.Log("Port %s is %d (players connect through the Minecraft router on %d)", t.Port.Name, n, a.routerPort())
			return
		}
	}
	ports[t.Port.Name] = a.routerPort()
}
