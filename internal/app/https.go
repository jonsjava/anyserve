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

// Sites lists every instance with a domain.
func (a *App) Sites() ([]https.Site, error) {
	list, _, err := a.Store.List()
	if err != nil {
		return nil, err
	}
	var out []https.Site
	for _, in := range list {
		if in.Domain == "" {
			continue
		}
		_, d, err := a.Store.Load(in.Name)
		if err != nil {
			continue
		}
		site, err := siteFor(in, d)
		if err != nil {
			a.Log("Skipping %s's domain: %v", in.Name, err)
			continue
		}
		out = append(out, site)
	}
	return out, nil
}

func siteFor(in *instance.Instance, d *spec.Definition) (https.Site, error) {
	p, err := WebPort(d, in.DomainPort)
	if err != nil {
		return https.Site{}, err
	}
	ports, err := d.ResolvePorts(in.Ports)
	if err != nil {
		return https.Site{}, err
	}
	return https.Site{Domain: in.Domain, Port: ports[p.Name], Instance: in.Name}, nil
}

// checkDomain validates a domain change for an instance.
func (a *App) checkDomain(in *instance.Instance, d *spec.Definition, domain, port string) error {
	if domain == "" {
		return nil
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.HTTPSProvider() == https.None {
		return errors.New("the HTTPS proxy is off (`anyserve https use caddy` turns it on)")
	}
	if err := https.ValidDomain(domain); err != nil {
		return err
	}
	if _, err := WebPort(d, port); err != nil {
		return err
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
func (a *App) SyncHTTPS() error {
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
	return a.SyncHTTPS()
}

// HTTPSRunning reports whether Anyserve's Caddy is running.
func (a *App) HTTPSRunning() bool { return https.Managed{Dir: a.httpsDir()}.Running() }
