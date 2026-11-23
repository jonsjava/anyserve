package app

import (
	"fmt"
	"net"
	"strconv"

	"github.com/jonsjava/anyserve/internal/spec"
)

// DNSRecord is a record to create at the domain's DNS provider.
type DNSRecord struct {
	Name  string
	Type  string
	Value string
}

// DNSAdvice is what to create for an instance's domain, and how people
// then connect.
type DNSAdvice struct {
	Records []DNSRecord
	// Connect says what players or users type.
	Connect []string
	Notes   []string
}

// publicAddrs returns this machine's public (globally routable, not
// private) addresses, without asking any outside service.
func publicAddrs() (v4, v6 []string) {
	ifs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, nil
	}
	for _, a := range ifs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP
		if !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		if ip.To4() != nil {
			v4 = append(v4, ip.String())
		} else {
			v6 = append(v6, ip.String())
		}
	}
	return v4, v6
}

// DNS works out the records for an instance's domain.
func (a *App) DNS(name string) (DNSAdvice, error) {
	in, d, err := a.Store.Load(name)
	if err != nil {
		return DNSAdvice{}, err
	}
	if in.Domain == "" {
		return DNSAdvice{}, fmt.Errorf("%s has no domain (`anyserve config %s --domain play.example.com`)", name, name)
	}
	var adv DNSAdvice
	v4, v6 := publicAddrs()
	for _, ip := range v4 {
		adv.Records = append(adv.Records, DNSRecord{in.Domain, "A", ip})
	}
	for _, ip := range v6 {
		adv.Records = append(adv.Records, DNSRecord{in.Domain, "AAAA", ip})
	}
	ports, err := d.ResolvePorts(in.Ports)
	if err != nil {
		return DNSAdvice{}, err
	}
	t, err := TargetFor(d, in.DomainPort)
	if err != nil {
		return DNSAdvice{}, err
	}
	if len(v4) == 0 {
		adv.Records = append(adv.Records, DNSRecord{in.Domain, "A", "<your public IPv4 address>"})
		forward := "the ports in `anyserve ports " + name + "`"
		switch t.Kind {
		case spec.RouteHTTP, spec.RouteTLS:
			forward = "ports 80 and 443"
		case spec.RouteMinecraft:
			forward = fmt.Sprintf("port %d/tcp (the Minecraft router, shared by every server with a domain)", a.routerPort())
		}
		adv.Notes = append(adv.Notes, "This machine has no public IPv4 address of its own, so it's probably behind a router: use the router's public address and forward "+forward+" to this machine.")
	}
	for _, p := range d.Ports {
		if p.SRV == "" {
			continue
		}
		port := ports[p.Name]
		if t.Kind == spec.RouteMinecraft && p.Name == t.Port.Name {
			continue // the router answers on the standard port
		}
		adv.Records = append(adv.Records, DNSRecord{p.SRV + "." + in.Domain, "SRV", fmt.Sprintf("0 5 %d %s.", port, in.Domain)})
	}
	switch t.Kind {
	case spec.RouteHTTP, spec.RouteTLS:
		adv.Connect = append(adv.Connect, "https://"+in.Domain)
	case spec.RouteMinecraft:
		adv.Connect = append(adv.Connect, in.Domain+" (the Minecraft router on port "+strconv.Itoa(a.routerPort())+" sends players here)")
	default:
		for _, p := range d.Ports {
			if !p.Public {
				continue
			}
			addr := in.Domain + ":" + strconv.Itoa(ports[p.Name])
			if p.SRV != "" {
				addr = in.Domain + " (found through the SRV record)"
			}
			adv.Connect = append(adv.Connect, fmt.Sprintf("%s: %s", p.Name, addr))
		}
		adv.Notes = append(adv.Notes, "This server's protocol can't be routed by name, so the domain is a DNS name for this machine; its ports still need forwarding.")
	}
	return adv, nil
}
