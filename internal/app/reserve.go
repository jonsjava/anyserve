package app

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PortReservation keeps a range of ports for something Anyserve doesn't
// run, such as another panel's servers that are stopped right now, so
// installs never pick them.
type PortReservation struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	Protocol string `json:"protocol"` // tcp, udp or both
	Note     string `json:"note,omitempty"`
}

func (r PortReservation) String() string {
	s := strconv.Itoa(r.From)
	if r.To != r.From {
		s += "-" + strconv.Itoa(r.To)
	}
	return s + "/" + r.Protocol
}

// Covers reports whether the reservation holds port n for proto.
func (r PortReservation) Covers(n int, proto string) bool {
	return n >= r.From && n <= r.To && protocolsOverlap(r.Protocol, proto)
}

// ParsePortRange reads "25565" or "25565-25575".
func ParsePortRange(s string) (from, to int, err error) {
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "-")
	if from, err = strconv.Atoi(strings.TrimSpace(a)); err != nil {
		return 0, 0, fmt.Errorf("%q isn't a port or a range like 27015-27030", s)
	}
	to = from
	if isRange {
		if to, err = strconv.Atoi(strings.TrimSpace(b)); err != nil {
			return 0, 0, fmt.Errorf("%q isn't a port or a range like 27015-27030", s)
		}
	}
	if from < 1 || to > 65535 || from > to {
		return 0, 0, fmt.Errorf("%q: ports are 1-65535, lowest first", s)
	}
	return from, to, nil
}

// Reservations lists the reserved port ranges.
func (a *App) Reservations() ([]PortReservation, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	return cfg.Reserved, nil
}

// Reserve adds a reservation. It returns the instances already using
// ports in it, which keep them.
func (a *App) Reserve(r PortReservation) (inUse []string, err error) {
	if r.Protocol == "" {
		r.Protocol = "both"
	}
	if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "both" {
		return nil, fmt.Errorf("protocol must be tcp, udp or both (got %q)", r.Protocol)
	}
	if r.From < 1 || r.To > 65535 || r.From > r.To {
		return nil, errors.New("ports are 1-65535, lowest first")
	}
	if strings.ContainsAny(r.Note, "\r\n") || len(r.Note) > 200 {
		return nil, errors.New("the note must be one line of at most 200 characters")
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	for _, o := range cfg.Reserved {
		if o.From == r.From && o.To == r.To && o.Protocol == r.Protocol {
			return nil, fmt.Errorf("%s is already reserved", r)
		}
	}
	cfg.Reserved = append(cfg.Reserved, r)
	sort.SliceStable(cfg.Reserved, func(i, j int) bool { return cfg.Reserved[i].From < cfg.Reserved[j].From })
	if err := a.SaveConfig(cfg); err != nil {
		return nil, err
	}
	return a.instancesIn(r), nil
}

// Unreserve removes the reservations of exactly from-to (any protocol).
func (a *App) Unreserve(from, to int) (removed int, err error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return 0, err
	}
	kept := cfg.Reserved[:0]
	for _, r := range cfg.Reserved {
		if r.From == from && r.To == to {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	if removed == 0 {
		have := make([]string, len(cfg.Reserved))
		for i, r := range cfg.Reserved {
			have[i] = r.String()
		}
		if len(have) == 0 {
			return 0, errors.New("no ports are reserved")
		}
		return 0, fmt.Errorf("nothing is reserved as exactly %s (reserved: %s)", PortReservation{From: from, To: to, Protocol: "any"}, strings.Join(have, ", "))
	}
	cfg.Reserved = kept
	return removed, a.SaveConfig(cfg)
}

// reservedFor returns the reservation holding port n for proto, if any.
func reservedFor(rs []PortReservation, n int, proto string) (PortReservation, bool) {
	for _, r := range rs {
		if r.Covers(n, proto) {
			return r, true
		}
	}
	return PortReservation{}, false
}

// instancesIn lists "name (port N)" for instance ports inside r.
func (a *App) instancesIn(r PortReservation) []string {
	var out []string
	list, _, _ := a.Store.List()
	for _, o := range list {
		_, d, err := a.Store.Load(o.Name)
		if err != nil {
			continue
		}
		ports, err := d.ResolvePorts(o.Ports)
		if err != nil {
			continue
		}
		for _, p := range d.Ports {
			if r.Covers(ports[p.Name], p.Protocol) {
				out = append(out, fmt.Sprintf("%s (port %d)", o.Name, ports[p.Name]))
			}
		}
	}
	return out
}

func describe(r PortReservation) string {
	if r.Note != "" {
		return fmt.Sprintf("reserved (%s: %s)", r, r.Note)
	}
	return fmt.Sprintf("reserved (%s)", r)
}
