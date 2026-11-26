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
		if _, set := given[k]; set || !ok || s.Address == nil {
			continue
		}
		keep = append(keep, k)
		if s.Address.Once {
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
