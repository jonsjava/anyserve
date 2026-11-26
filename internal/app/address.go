package app

import (
	"net"
	"strconv"
	"strings"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// hostAddress is the address other machines reach this one at: its public
// IPv4 address, else its LAN address, else localhost. Nothing is sent to
// work it out.
var hostAddress = func() string {
	if v4, _ := publicAddrs(); len(v4) > 0 {
		return v4[0]
	}
	// Connecting a UDP socket sends nothing; it only picks the address
	// this machine would use to reach the internet, which is its LAN one.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	return "localhost"
}

// proxiedPort is the port the HTTPS proxy serves for a domain, or "" when
// there's no domain or it doesn't go through the proxy.
func proxiedPort(d *spec.Definition, domain, domainPort string) string {
	if domain == "" {
		return ""
	}
	t, err := TargetFor(d, domainPort)
	if err != nil || t.Kind != spec.RouteHTTP {
		return ""
	}
	return t.Port.Name
}

// PortURL is the address people reach a port at: https://<domain> when the
// HTTPS proxy serves it, else http://<this machine>:<port>.
func PortURL(d *spec.Definition, port string, ports map[string]int, domain, domainPort string) string {
	if port == proxiedPort(d, domain, domainPort) {
		return "https://" + domain
	}
	return "http://" + net.JoinHostPort(hostAddress(), strconv.Itoa(ports[port]))
}

// addressValue writes an address setting's value.
func addressValue(d *spec.Definition, ad *spec.Address, ports map[string]int, domain, domainPort string) string {
	u := PortURL(d, ad.Port, ports, domain, domainPort)
	if ad.Form == "host" {
		host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		return host
	}
	return u + ad.Path
}

// fillAddresses sets each address setting the user didn't give, and
// returns their keys.
func fillAddresses(d *spec.Definition, given map[string]string, settings map[string]any, ports map[string]int, domain, domainPort string) []string {
	var filled []string
	for _, s := range d.Settings {
		if s.Address == nil {
			continue
		}
		if _, ok := given[s.Key]; ok {
			continue
		}
		settings[s.Key] = addressValue(d, s.Address, ports, domain, domainPort)
		filled = append(filled, s.Key)
	}
	return filled
}

// refreshAddresses brings an instance's filled-in address settings up to
// date after its ports or domain change, unless the user has since set one
// (in given) or the app can't change it after its first start. It returns
// the settings it changed.
func refreshAddresses(in *instance.Instance, d *spec.Definition, given map[string]string) []string {
	var changed, keep []string
	for _, k := range in.AutoAddress {
		s, ok := d.Setting(k)
		if _, set := given[k]; set || !ok {
			continue
		}
		keep = append(keep, k)
		// An instance still on a definition from before address hints
		// (filled in by MigrateReachable) keeps the key for later.
		if s.Address == nil || s.Address.Once {
			continue
		}
		if v := addressValue(d, s.Address, in.Ports, in.Domain, in.DomainPort); v != in.Settings[k] {
			in.Settings[k] = v
			changed = append(changed, k)
		}
	}
	in.AutoAddress = keep
	return changed
}

// defaultExpose is what a stack opens to the network when the user doesn't
// say: every private port, since people install an app to use it from
// elsewhere, except the one the HTTPS proxy serves (it reaches that one on
// this machine).
func defaultExpose(d *spec.Definition, domain, domainPort string) []string {
	if !d.IsStack() {
		return nil
	}
	proxied := proxiedPort(d, domain, domainPort)
	var out []string
	for _, p := range d.Ports {
		if !p.Public && p.Name != proxied {
			out = append(out, p.Name)
		}
	}
	return out
}

// Addresses lists where people reach an instance's apps: each port opened
// to the network or served by the HTTPS proxy, as name and address. Ports
// the server itself listens on publicly (game ports) aren't included.
func Addresses(in *instance.Instance, d *spec.Definition, ports map[string]int) [][2]string {
	if !d.IsStack() {
		return nil
	}
	proxied := proxiedPort(d, in.Domain, in.DomainPort)
	var out [][2]string
	for _, p := range d.Ports {
		if p.Public || p.Protocol == "udp" {
			continue
		}
		switch {
		case p.Name == proxied || in.Exposed(p.Name) && p.Name != "ssh":
			out = append(out, [2]string{p.Name, PortURL(d, p.Name, ports, in.Domain, in.DomainPort)})
		case in.Exposed(p.Name):
			out = append(out, [2]string{p.Name, "ssh://" + net.JoinHostPort(hostAddress(), strconv.Itoa(ports[p.Name]))})
		}
	}
	return out
}

// MigrationReachable is the one-time change that brings apps installed
// before Anyserve opened them to the network up to date.
const MigrationReachable = "reachable"

// migrateReachable applies MigrationReachable to one instance in memory:
// it opens the app's private ports (if the user never chose any), and
// fills in address settings still empty or at an old localhost default.
// hints is the newest definition, whose address hints the instance's own
// (older) definition may lack. It reports what changed.
func migrateReachable(in *instance.Instance, d, hints *spec.Definition) []string {
	var changes []string
	if len(in.Expose) == 0 {
		if open := defaultExpose(d, in.Domain, in.DomainPort); len(open) > 0 {
			in.Expose = open
			changes = append(changes, "opened "+strings.Join(open, ", ")+" to the network")
		}
	}
	ports, err := d.ResolvePorts(in.Ports)
	if err != nil {
		ports = in.Ports
	}
	auto := map[string]bool{}
	for _, k := range in.AutoAddress {
		auto[k] = true
	}
	for _, h := range hints.Settings {
		if h.Address == nil || h.Address.Once || auto[h.Key] {
			continue
		}
		s, ok := d.Setting(h.Key)
		if !ok || s.Type != "string" {
			continue
		}
		if _, ok := d.Port(h.Address.Port); !ok {
			continue
		}
		// Only a value nobody chose: empty, or a default either version of
		// the definition shipped.
		v := in.Settings[h.Key]
		if v != "" && v != s.DefaultString() && v != h.DefaultString() {
			continue
		}
		nv := addressValue(d, h.Address, ports, in.Domain, in.DomainPort)
		in.AutoAddress = append(in.AutoAddress, h.Key)
		if nv != v {
			if in.Settings == nil {
				in.Settings = map[string]string{}
			}
			in.Settings[h.Key] = nv
			changes = append(changes, "set "+h.Key+" to "+nv)
		}
	}
	in.Migrations = append(in.Migrations, MigrationReachable)
	return changes
}

// MigrateReachable applies MigrationReachable to every app that hasn't
// had it, restarting the running ones so the change takes effect.
// Anyserve's service runs it when it starts.
func (a *App) MigrateReachable() {
	list, _, err := a.Store.List()
	if err != nil {
		a.Log("Couldn't check apps for the network change: %v", err)
		return
	}
	for _, in := range list {
		if in.Migrated(MigrationReachable) {
			continue
		}
		if err := a.migrateOne(in.Name); err != nil {
			a.Log("Couldn't open %s to the network: %v", in.Name, err)
		}
	}
}

func (a *App) migrateOne(name string) error {
	in, d, t, err := a.load(name)
	if err != nil {
		return err
	}
	if !d.IsStack() {
		in.Migrations = append(in.Migrations, MigrationReachable)
		return a.Store.Save(in, d)
	}
	hints := d
	if e, err := a.Find(in.Registry + "/" + in.Service); err == nil {
		hints = e.Def
	}
	changes := migrateReachable(in, d, hints)
	if err := a.Store.Save(in, d); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	a.Log("%s: %s", name, strings.Join(changes, "; "))
	if err := a.writeConfig(in, d); err != nil {
		return err
	}
	if err := t.Register(in, d, a.targetEnv()); err != nil {
		return err
	}
	if t.Status(in).State == "running" {
		a.Log("Restarting %s to apply the change", name)
		if err := t.Stop(in); err != nil {
			return err
		}
		return t.Start(in)
	}
	return nil
}
